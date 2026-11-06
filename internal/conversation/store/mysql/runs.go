package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

// CreateRunTx inserts the user message and the queued run atomically. The
// caller enqueues async_jobs in the same transaction.
func (s *Store) CreateRunTx(ctx context.Context, tx *sql.Tx, run domain.SearchRun, message *domain.Message, messageKey, messageHash string, now time.Time) error {
	if message != nil {
		if err := insertMessageTx(ctx, tx, *message, messageKey, messageHash); err != nil {
			return err
		}
		if err := touchConversationTx(ctx, tx, run.OwnerID, run.ConversationID, message.Role, now); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conv_search_runs
		  (id, owner_id, conversation_id, use_case_revision_id, source_sku_id, source_quote_id,
		   input, status, platform_status, reply, request_key, attempts, error,
		   started_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(run.ID), run.OwnerID, db.UUIDBytes(run.ConversationID),
		nullableUUID(run.UseCaseRevisionID), nullableUUID(run.SourceSKUID), nullableUUID(run.SourceQuoteID),
		string(run.Input), run.Status, string(run.PlatformStatus), run.Reply, run.RequestKey,
		run.Attempts, run.Error, run.StartedAt, run.ExpiresAt, now)
	return err
}

func (s *Store) GetRun(ctx context.Context, ownerID string, runID uuid.UUID) (*domain.SearchRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(runID), ownerID))
}

func (s *Store) GetRunByRequestKey(ctx context.Context, ownerID, requestKey string) (*domain.SearchRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelect+` WHERE owner_id = ? AND request_key = ?`,
		ownerID, requestKey))
}

func (s *Store) ListAssessments(ctx context.Context, ownerID string, runID uuid.UUID) ([]domain.Assessment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_id, search_run_id, source_quote_id, candidate_quote_id, candidate_sku_id,
		       relation_kind, verdict, constraint_checks, cost_breakdown, reason, engine_version, created_at
		FROM conv_substitution_assessments
		WHERE owner_id = ? AND search_run_id = ?
		ORDER BY id ASC`, ownerID, db.UUIDBytes(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Assessment
	for rows.Next() {
		assessment, err := scanAssessment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *assessment)
	}
	return out, rows.Err()
}

// SetRunRunning marks a queued run as running. Re-running an already succeeded
// run is a no-op so worker retries are idempotent.
func (s *Store) SetRunRunning(ctx context.Context, ownerID string, runID uuid.UUID, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE conv_search_runs
		SET status = 'running', attempts = attempts + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND status IN ('queued','running','failed')`,
		now, db.UUIDBytes(runID), ownerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	run, err := s.GetRun(ctx, ownerID, runID)
	if err != nil {
		return err
	}
	if run.Status == domain.RunSucceeded {
		return nil
	}
	return domain.ErrRunCancelled
}

// CancelRun is idempotent: a terminal run is returned unchanged.
func (s *Store) CancelRun(ctx context.Context, ownerID string, runID uuid.UUID, now time.Time) (*domain.SearchRun, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE conv_search_runs
		SET status = 'cancelled', finished_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND status IN ('queued','running')`,
		now, now, db.UUIDBytes(runID), ownerID); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, ownerID, runID)
}

// CompleteRunTx writes assessments, the assistant message and the terminal
// succeeded status atomically. A cancelled run is not overwritten.
func (s *Store) CompleteRunTx(ctx context.Context, tx *sql.Tx, ownerID string, runID uuid.UUID,
	reply string, assessments []domain.Assessment, message *domain.Message, messageKey string, now time.Time) error {
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT status FROM conv_search_runs WHERE id = ? AND owner_id = ? FOR UPDATE`,
		db.UUIDBytes(runID), ownerID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == domain.RunCancelled {
		return domain.ErrRunCancelled
	}
	if status == domain.RunSucceeded {
		return nil
	}
	for _, assessment := range assessments {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conv_substitution_assessments
			  (id, owner_id, search_run_id, source_quote_id, candidate_quote_id, candidate_sku_id,
			   relation_kind, verdict, constraint_checks, cost_breakdown, reason, engine_version, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
			  verdict = VALUES(verdict), constraint_checks = VALUES(constraint_checks),
			  cost_breakdown = VALUES(cost_breakdown), reason = VALUES(reason)`,
			db.UUIDBytes(assessment.ID), ownerID, db.UUIDBytes(runID),
			nullableUUID(assessment.SourceQuoteID), nullableUUID(assessment.CandidateQuoteID),
			nullableUUID(assessment.CandidateSKUID), assessment.RelationKind, assessment.Verdict,
			string(assessment.ConstraintChecks), string(assessment.CostBreakdown), assessment.Reason,
			assessment.EngineVersion, assessment.CreatedAt); err != nil {
			return err
		}
	}
	if message != nil {
		// The run id is the idempotency key; ON DUPLICATE keeps retries safe.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conv_messages (id, conversation_id, role, content, create_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE content = VALUES(content)`,
			db.UUIDBytes(message.ID), db.UUIDBytes(message.ConversationID), message.Role,
			message.Content, messageKey, message.CreatedAt); err != nil {
			return err
		}
		if err := touchConversationTx(ctx, tx, ownerID, message.ConversationID, message.Role, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE conv_search_runs
		SET status = 'succeeded', reply = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ?`, reply, now, now, db.UUIDBytes(runID), ownerID)
	return err
}

func (s *Store) FailRun(ctx context.Context, ownerID string, runID uuid.UUID, reason string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conv_search_runs
		SET status = 'failed', error = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND status IN ('queued','running')`,
		truncate(reason, 512), now, now, db.UUIDBytes(runID), ownerID)
	return err
}

const runSelect = `SELECT id, owner_id, conversation_id, use_case_revision_id, source_sku_id, source_quote_id,
	input, status, platform_status, reply, request_key, attempts, error,
	started_at, finished_at, expires_at, updated_at
	FROM conv_search_runs`

func scanRun(row interface{ Scan(...any) error }) (*domain.SearchRun, error) {
	var run domain.SearchRun
	var rawID, rawConversation []byte
	var revisionID, sourceSKU, sourceQuote []byte
	var input, platformStatus []byte
	var finished sql.NullTime
	if err := row.Scan(&rawID, &run.OwnerID, &rawConversation, &revisionID, &sourceSKU, &sourceQuote,
		&input, &run.Status, &platformStatus, &run.Reply, &run.RequestKey, &run.Attempts,
		&run.Error, &run.StartedAt, &finished, &run.ExpiresAt, &run.UpdatedAt); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	run.ID = parsed
	conversation, err := db.UUIDFromBytes(rawConversation)
	if err != nil {
		return nil, err
	}
	run.ConversationID = conversation
	if run.UseCaseRevisionID, err = db.UUIDPtrFromBytes(revisionID); err != nil {
		return nil, err
	}
	if run.SourceSKUID, err = db.UUIDPtrFromBytes(sourceSKU); err != nil {
		return nil, err
	}
	if run.SourceQuoteID, err = db.UUIDPtrFromBytes(sourceQuote); err != nil {
		return nil, err
	}
	run.Input = input
	run.PlatformStatus = platformStatus
	if finished.Valid {
		run.FinishedAt = &finished.Time
	}
	return &run, nil
}

func scanAssessment(row interface{ Scan(...any) error }) (*domain.Assessment, error) {
	var assessment domain.Assessment
	var rawID, rawRun, sourceQuote, candidateQuote, candidateSKU []byte
	var checks, breakdown []byte
	if err := row.Scan(&rawID, &assessment.OwnerID, &rawRun, &sourceQuote, &candidateQuote, &candidateSKU,
		&assessment.RelationKind, &assessment.Verdict, &checks, &breakdown,
		&assessment.Reason, &assessment.EngineVersion, &assessment.CreatedAt); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	assessment.ID = parsed
	run, err := db.UUIDFromBytes(rawRun)
	if err != nil {
		return nil, err
	}
	assessment.SearchRunID = run
	if assessment.SourceQuoteID, err = db.UUIDPtrFromBytes(sourceQuote); err != nil {
		return nil, err
	}
	if assessment.CandidateQuoteID, err = db.UUIDPtrFromBytes(candidateQuote); err != nil {
		return nil, err
	}
	if assessment.CandidateSKUID, err = db.UUIDPtrFromBytes(candidateSKU); err != nil {
		return nil, err
	}
	assessment.ConstraintChecks = checks
	assessment.CostBreakdown = breakdown
	return &assessment, nil
}

func nullableUUID(value *uuid.UUID) any {
	if value == nil {
		return nil
	}
	return db.UUIDBytes(*value)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
