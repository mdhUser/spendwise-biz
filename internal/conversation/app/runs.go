package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	"github.com/mdhuser/spendwise-biz/internal/jobs"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

// RunEnqueuer is the subset of jobs.Queue used by the run API. It is an
// interface so the transaction can be composed and tests can fake it.
type RunEnqueuer interface {
	Enqueue(context.Context, *sql.Tx, *jobs.Job) error
	CancelByRun(context.Context, string, uuid.UUID) error
}

func (s *Service) SetQueue(queue RunEnqueuer) { s.queue = queue }

type CreateRunInput struct {
	ConversationID  string   `json:"conversationId"`
	Message         string   `json:"message"`
	SourceSKUID     string   `json:"sourceSkuId"`
	CandidateSKUIDs []string `json:"candidateSkuIds"`
}

type AssessmentInput struct {
	SourceQuoteID    *string         `json:"sourceQuoteId"`
	CandidateQuoteID *string         `json:"candidateQuoteId"`
	CandidateSKUID   *string         `json:"candidateSkuId"`
	RelationKind     string          `json:"relationKind"`
	Verdict          string          `json:"verdict"`
	ConstraintChecks json.RawMessage `json:"constraintChecks"`
	CostBreakdown    json.RawMessage `json:"costBreakdown"`
	Reason           string          `json:"reason"`
}

type RunView struct {
	ID             string           `json:"id"`
	JobID          string           `json:"jobId,omitempty"`
	ConversationID string           `json:"conversationId"`
	Status         string           `json:"status"`
	Reply          string           `json:"reply"`
	Error          string           `json:"error,omitempty"`
	SourceSKUID    *string          `json:"sourceSkuId,omitempty"`
	StartedAt      time.Time        `json:"startedAt"`
	FinishedAt     *time.Time       `json:"finishedAt,omitempty"`
	Assessments    []AssessmentView `json:"assessments"`
}

type AssessmentView struct {
	ID             string          `json:"id"`
	CandidateSKUID *string         `json:"candidateSkuId,omitempty"`
	Verdict        string          `json:"verdict"`
	Reason         string          `json:"reason"`
	CostBreakdown  json.RawMessage `json:"costBreakdown"`
}

type RunJob struct {
	RunID           uuid.UUID
	ConversationID  uuid.UUID
	OwnerID         string
	SourceSKUID     string
	CandidateSKUIDs []string
	Message         string
}

// CreateRun validates and persists the user message, search run and queued job
// in one transaction, then returns immediately for a 202 response.
func (s *Service) CreateRun(ctx context.Context, ownerID, idempotencyKey string, input CreateRunInput) (RunView, bool, error) {
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return RunView{}, false, err
	}
	conversationID, err := parseID(input.ConversationID)
	if err != nil {
		return RunView{}, false, err
	}
	content := strings.TrimSpace(input.Message)
	if content == "" || len([]rune(content)) > domain.MaxContentRunes {
		return RunView{}, false, apperr.Validation("message 长度必须在 1 到 4000 个字符之间")
	}
	sourceSKUID, err := optionalUUID(input.SourceSKUID, "sourceSkuId")
	if err != nil {
		return RunView{}, false, err
	}
	candidates, err := parseCandidateIDs(input.CandidateSKUIDs)
	if err != nil {
		return RunView{}, false, err
	}
	sourceValue := ""
	if sourceSKUID != nil {
		sourceValue = sourceSKUID.String()
	}
	candidateValues := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateValues = append(candidateValues, candidate.String())
	}
	if _, err := s.store.GetConversation(ctx, ownerID, conversationID); err != nil {
		return RunView{}, false, mapError(err)
	}
	if s.queue == nil {
		return RunView{}, false, apperr.New(503, "UNAVAILABLE", "异步任务队列尚未就绪")
	}
	digest := requestDigest("run.create", input)
	existing, findErr := s.store.GetRunByRequestKey(ctx, ownerID, key)
	if findErr == nil {
		if runRequestHash(*existing) != digest {
			return RunView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
		}
		view, buildErr := s.runView(ctx, ownerID, *existing)
		if buildErr != nil {
			return RunView{}, false, buildErr
		}
		return view, true, nil
	}
	if !errors.Is(findErr, domain.ErrNotFound) {
		return RunView{}, false, findErr
	}

	now := s.now().UTC().Truncate(time.Millisecond)
	runID := id.New()
	messageID := id.New()
	storedInput := runInput{
		ConversationID: conversationID.String(), Message: content,
		SourceSKUID: sourceValue, CandidateSKUIDs: candidateValues, RequestHash: digest,
	}
	inputJSON, err := json.Marshal(storedInput)
	if err != nil {
		return RunView{}, false, err
	}
	payload, err := json.Marshal(map[string]any{
		"runId": runID.String(), "conversationId": conversationID.String(),
		"sourceSkuId": sourceValue, "candidateSkuIds": candidateValues,
	})
	if err != nil {
		return RunView{}, false, err
	}
	run := domain.SearchRun{
		ID: runID, OwnerID: ownerID, ConversationID: conversationID,
		SourceSKUID: sourceSKUID, Input: inputJSON, Status: domain.RunQueued,
		PlatformStatus: json.RawMessage(`[]`), RequestKey: key,
		StartedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour), UpdatedAt: now,
	}
	message := domain.Message{ID: messageID, ConversationID: conversationID, Role: "user", Content: content, CreatedAt: now}
	job := &jobs.Job{
		ID: id.New(), Kind: "run.substitution", OwnerID: ownerID, RunID: &runID,
		Payload: payload, MaxAttempts: 3, RunAfter: now, CreatedAt: now,
	}
	if err := s.store.WithTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.CreateRunTx(ctx, tx, run, &message, key, digest, now); err != nil {
			return err
		}
		return s.queue.Enqueue(ctx, tx, job)
	}); err != nil {
		if db.IsDuplicate(err) {
			existing, findErr := s.store.GetRunByRequestKey(ctx, ownerID, key)
			if findErr == nil && runRequestHash(*existing) == digest {
				view, buildErr := s.runView(ctx, ownerID, *existing)
				if buildErr == nil {
					return view, true, nil
				}
			}
			return RunView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
		}
		return RunView{}, false, err
	}
	return RunView{
		ID: runID.String(), JobID: job.ID.String(), ConversationID: conversationID.String(),
		Status: domain.RunQueued, StartedAt: now, Assessments: []AssessmentView{},
	}, false, nil
}

func (s *Service) GetRun(ctx context.Context, ownerID, rawID string) (RunView, error) {
	runID, err := parseID(rawID)
	if err != nil {
		return RunView{}, err
	}
	run, err := s.store.GetRun(ctx, ownerID, runID)
	if err != nil {
		return RunView{}, mapError(err)
	}
	return s.runView(ctx, ownerID, *run)
}

func (s *Service) CancelRun(ctx context.Context, ownerID, rawID string) (RunView, error) {
	runID, err := parseID(rawID)
	if err != nil {
		return RunView{}, err
	}
	run, err := s.store.CancelRun(ctx, ownerID, runID, s.now().UTC().Truncate(time.Millisecond))
	if err != nil {
		return RunView{}, mapError(err)
	}
	if s.queue != nil {
		// Best effort: the run is already terminal; an orphan job will be
		// claimed and immediately see ErrRunCancelled.
		_ = s.queue.CancelByRun(ctx, ownerID, runID)
	}
	return s.runView(ctx, ownerID, *run)
}

// RunJob returns the parsed input a worker needs. It deliberately does not
// touch catalog tables: the worker receives catalog data through catalog's
// app.Service.
func (s *Service) RunJob(ctx context.Context, ownerID string, runID uuid.UUID) (RunJob, error) {
	run, err := s.store.GetRun(ctx, ownerID, runID)
	if err != nil {
		return RunJob{}, mapError(err)
	}
	var input runInput
	if err := json.Unmarshal(run.Input, &input); err != nil {
		return RunJob{}, err
	}
	job := RunJob{
		RunID: run.ID, ConversationID: run.ConversationID, OwnerID: ownerID,
		SourceSKUID: input.SourceSKUID, CandidateSKUIDs: input.CandidateSKUIDs, Message: input.Message,
	}
	return job, nil
}

func (s *Service) MarkRunRunning(ctx context.Context, ownerID string, runID uuid.UUID) error {
	return s.store.SetRunRunning(ctx, ownerID, runID, s.now().UTC().Truncate(time.Millisecond))
}

// CompleteRun is idempotent on the run id and never overwrites a cancellation.
func (s *Service) CompleteRun(ctx context.Context, ownerID string, runID uuid.UUID, reply string, assessments []AssessmentInput, assistantContent string) error {
	run, err := s.store.GetRun(ctx, ownerID, runID)
	if err != nil {
		return mapError(err)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	domainAssessments := make([]domain.Assessment, 0, len(assessments))
	for _, item := range assessments {
		sourceQuoteID, err := optionalUUIDPtr(item.SourceQuoteID)
		if err != nil {
			return err
		}
		candidateQuoteID, err := optionalUUIDPtr(item.CandidateQuoteID)
		if err != nil {
			return err
		}
		candidateSKUID, err := optionalUUIDPtr(item.CandidateSKUID)
		if err != nil {
			return err
		}
		checks := item.ConstraintChecks
		if len(checks) == 0 {
			checks = json.RawMessage(`{}`)
		}
		breakdown := item.CostBreakdown
		if len(breakdown) == 0 {
			breakdown = json.RawMessage(`{}`)
		}
		domainAssessments = append(domainAssessments, domain.Assessment{
			ID: id.New(), OwnerID: ownerID, SearchRunID: runID,
			SourceQuoteID: sourceQuoteID, CandidateQuoteID: candidateQuoteID, CandidateSKUID: candidateSKUID,
			RelationKind: item.RelationKind, Verdict: item.Verdict,
			ConstraintChecks: checks, CostBreakdown: breakdown, Reason: item.Reason,
			EngineVersion: 1, CreatedAt: now,
		})
	}
	message := &domain.Message{
		ID: id.New(), ConversationID: run.ConversationID, Role: "assistant",
		Content: assistantContent, CreatedAt: now,
	}
	return s.store.WithTx(ctx, func(tx *sql.Tx) error {
		return s.store.CompleteRunTx(ctx, tx, ownerID, runID, reply, domainAssessments, message, "run:"+runID.String()+":assistant", now)
	})
}

func (s *Service) FailRun(ctx context.Context, ownerID string, runID uuid.UUID, reason string) error {
	return s.store.FailRun(ctx, ownerID, runID, reason, s.now().UTC().Truncate(time.Millisecond))
}

func (s *Service) runView(ctx context.Context, ownerID string, run domain.SearchRun) (RunView, error) {
	assessments, err := s.store.ListAssessments(ctx, ownerID, run.ID)
	if err != nil {
		return RunView{}, err
	}
	view := RunView{
		ID: run.ID.String(), ConversationID: run.ConversationID.String(), Status: run.Status,
		Reply: run.Reply, Error: run.Error, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Assessments: make([]AssessmentView, 0, len(assessments)),
	}
	if run.SourceSKUID != nil {
		value := run.SourceSKUID.String()
		view.SourceSKUID = &value
	}
	for _, assessment := range assessments {
		item := AssessmentView{
			ID: assessment.ID.String(), Verdict: assessment.Verdict,
			Reason: assessment.Reason, CostBreakdown: assessment.CostBreakdown,
		}
		if assessment.CandidateSKUID != nil {
			value := assessment.CandidateSKUID.String()
			item.CandidateSKUID = &value
		}
		view.Assessments = append(view.Assessments, item)
	}
	return view, nil
}

type runInput struct {
	ConversationID  string   `json:"conversationId"`
	Message         string   `json:"message"`
	SourceSKUID     string   `json:"sourceSkuId"`
	CandidateSKUIDs []string `json:"candidateSkuIds"`
	RequestHash     string   `json:"requestHash,omitempty"`
}

func runRequestHash(run domain.SearchRun) string {
	var input runInput
	if err := json.Unmarshal(run.Input, &input); err != nil {
		return ""
	}
	return input.RequestHash
}

func optionalUUID(value, field string) (*uuid.UUID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil, apperr.Validation(field + " 不合法")
	}
	return &parsed, nil
}

func optionalUUIDPtr(value *string) (*uuid.UUID, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	return optionalUUID(*value, "id")
}

func parseCandidateIDs(values []string) ([]uuid.UUID, error) {
	if len(values) > 20 {
		return nil, apperr.Validation("candidateSkuIds 最多 20 个")
	}
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, apperr.Validation("candidateSkuIds 包含非法 id")
		}
		if seen[parsed] {
			continue
		}
		seen[parsed] = true
		out = append(out, parsed)
	}
	return out, nil
}
