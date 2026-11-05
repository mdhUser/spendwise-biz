package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

var (
	ErrIdempotencyInProgress = errors.New("idempotency request in progress")
	ErrIdempotencyMismatch   = errors.New("idempotency key reuse with different request")
)

type IdempotencyReplay struct {
	Status     int
	Body       []byte
	ResourceID *uuid.UUID
}

func (s *Store) ClaimIdempotency(ctx context.Context, tx *sql.Tx, ownerID string, key string, requestHash []byte, ttl time.Duration) (*IdempotencyReplay, bool, error) {
	expires := time.Now().UTC().Add(ttl)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_records
		(owner_id, idempotency_key, request_hash, state, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
		ownerID, key, requestHash, expires)
	if err == nil {
		return nil, false, nil
	}
	if !db.IsDuplicate(err) {
		return nil, false, err
	}

	var storedHash, body, resource []byte
	var state string
	var status sql.NullInt64
	row := tx.QueryRowContext(ctx, `
		SELECT request_hash, state, response_status, response_body, resource_id
		FROM idempotency_records
		WHERE owner_id = ? AND idempotency_key = ?
		FOR UPDATE`, ownerID, key)
	if err := row.Scan(&storedHash, &state, &status, &body, &resource); err != nil {
		return nil, false, err
	}
	if !bytes.Equal(storedHash, requestHash) {
		return nil, false, ErrIdempotencyMismatch
	}
	if state == "pending" {
		return nil, false, ErrIdempotencyInProgress
	}
	replay := &IdempotencyReplay{Status: int(status.Int64), Body: body}
	if resource != nil {
		idValue, err := db.UUIDFromBytes(resource)
		if err != nil {
			return nil, false, err
		}
		replay.ResourceID = &idValue
	}
	return replay, true, nil
}

func (s *Store) CompleteIdempotency(ctx context.Context, tx *sql.Tx, ownerID string, key string, status int, body any, resourceID *uuid.UUID) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var resource any
	if resourceID != nil {
		resource = db.UUIDBytes(*resourceID)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE idempotency_records
		SET state = 'completed', response_status = ?, response_body = ?,
		    resource_id = ?, updated_at = UTC_TIMESTAMP(3)
		WHERE owner_id = ? AND idempotency_key = ?`,
		status, raw, resource, ownerID, key)
	return err
}

type AuditEvent struct {
	OwnerID   string
	Entity    string
	EntityID  uuid.UUID
	Action    string
	ActorID   string
	RequestID string
	Before    any
	After     any
}

func (s *Store) InsertAudit(ctx context.Context, q queryer, event AuditEvent) error {
	if len(event.RequestID) > 64 {
		event.RequestID = event.RequestID[:64]
	}
	var before, after []byte
	var err error
	if event.Before != nil {
		before, err = json.Marshal(event.Before)
		if err != nil {
			return err
		}
	}
	if event.After != nil {
		after, err = json.Marshal(event.After)
		if err != nil {
			return err
		}
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO audit_events
		(id, owner_id, entity_type, entity_id, action_type, actor_user_id,
		 request_id, before_data, after_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(id.New()), event.OwnerID, event.Entity,
		db.UUIDBytes(event.EntityID), event.Action, event.ActorID,
		event.RequestID, nullableJSON(before), nullableJSON(after))
	return err
}

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// DeleteExpiredIdempotency removes records whose retention window has ended.
// It deletes in bounded batches to keep locks short when a backlog exists.
func (s *Store) DeleteExpiredIdempotency(ctx context.Context, now time.Time) (int64, error) {
	const batchSize = 1000
	var total int64
	for {
		result, err := s.db.ExecContext(ctx,
			`DELETE FROM idempotency_records WHERE expires_at < ? LIMIT ?`, now.UTC(), batchSize)
		if err != nil {
			return total, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected < batchSize {
			return total, nil
		}
	}
}
