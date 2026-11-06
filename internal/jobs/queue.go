// Package jobs implements the MySQL SKIP LOCKED queue and worker loop.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

var (
	ErrNoJob    = errors.New("jobs: no job available")
	ErrLockLost = errors.New("jobs: lock lost")
)

type Job struct {
	ID          uuid.UUID
	Kind        string
	OwnerID     string
	RunID       *uuid.UUID
	Payload     json.RawMessage
	Status      Status
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	LockedUntil *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Queue struct {
	db           *sql.DB
	workerID     string
	lockDuration time.Duration
	now          func() time.Time
}

func NewQueue(database *sql.DB, workerID string) *Queue {
	if workerID == "" {
		workerID = "worker"
	}
	return &Queue{db: database, workerID: workerID, lockDuration: 120 * time.Second, now: func() time.Time { return time.Now().UTC() }}
}

func (q *Queue) SetClock(now func() time.Time) { q.now = now }
func (q *Queue) WorkerID() string              { return q.workerID }

func (q *Queue) Enqueue(ctx context.Context, tx *sql.Tx, job *Job) error {
	if job.ID == uuid.Nil {
		job.ID = id.New()
	}
	now := q.now()
	if job.Status == "" {
		job.Status = StatusQueued
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = now
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if len(job.Payload) == 0 {
		job.Payload = json.RawMessage(`{}`)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO async_jobs
		  (id, kind, owner_id, run_id, payload, status, attempts, max_attempts, run_after, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(job.ID), job.Kind, job.OwnerID, nullableUUID(job.RunID), string(job.Payload),
		string(job.Status), job.Attempts, job.MaxAttempts, job.RunAfter, job.CreatedAt, now)
	return err
}

func (q *Queue) Claim(ctx context.Context) (*Job, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := q.now()
	row := tx.QueryRowContext(ctx, `
		SELECT id, kind, owner_id, run_id, payload, attempts, max_attempts, run_after, created_at
		FROM async_jobs
		WHERE status = 'queued' AND run_after <= ?
		ORDER BY run_after, id
		LIMIT 1 FOR UPDATE SKIP LOCKED`, now)
	job, err := scanClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, err
	}
	job.Attempts++
	lockedUntil := now.Add(q.lockDuration)
	if _, err := tx.ExecContext(ctx, `
		UPDATE async_jobs
		SET status = 'running', attempts = ?, locked_by = ?, locked_until = ?, updated_at = ?
		WHERE id = ?`, job.Attempts, q.workerID, lockedUntil, now, db.UUIDBytes(job.ID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.Status = StatusRunning
	job.LockedUntil = &lockedUntil
	return job, nil
}

func (q *Queue) Heartbeat(ctx context.Context, jobID uuid.UUID) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE async_jobs SET locked_until = ?, updated_at = ?
		WHERE id = ? AND locked_by = ? AND status = 'running'`,
		q.now().Add(q.lockDuration), q.now(), db.UUIDBytes(jobID), q.workerID)
	return err
}

func (q *Queue) Complete(ctx context.Context, jobID uuid.UUID) error {
	result, err := q.db.ExecContext(ctx, `
		UPDATE async_jobs
		SET status = 'succeeded', locked_by = NULL, locked_until = NULL, last_error = NULL, updated_at = ?
		WHERE id = ? AND locked_by = ? AND status = 'running'`, q.now(), db.UUIDBytes(jobID), q.workerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrLockLost
	}
	return nil
}

// Fail moves a running job back to queued with exponential backoff, or to
// failed after max_attempts. It returns whether the job will retry.
func (q *Queue) Fail(ctx context.Context, jobID uuid.UUID, jobErr error, delay time.Duration) (bool, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var attempts, maxAttempts int
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT attempts, max_attempts, status FROM async_jobs
		WHERE id = ? AND locked_by = ? FOR UPDATE`, db.UUIDBytes(jobID), q.workerID).
		Scan(&attempts, &maxAttempts, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrLockLost
	}
	if err != nil {
		return false, err
	}
	if status != string(StatusRunning) {
		return false, ErrLockLost
	}
	message := ""
	if jobErr != nil {
		message = truncate(jobErr.Error(), 512)
	}
	now := q.now()
	retry := attempts < maxAttempts
	if retry {
		if delay <= 0 {
			delay = Backoff(attempts)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE async_jobs
			SET status = 'queued', run_after = ?, locked_by = NULL, locked_until = NULL,
			    last_error = ?, updated_at = ?
			WHERE id = ?`, now.Add(delay), message, now, db.UUIDBytes(jobID)); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE async_jobs
			SET status = 'failed', locked_by = NULL, locked_until = NULL,
			    last_error = ?, updated_at = ?
			WHERE id = ?`, message, now, db.UUIDBytes(jobID)); err != nil {
			return false, err
		}
	}
	return retry, tx.Commit()
}

// CancelByRun marks queued/running jobs for a run as cancelled.
func (q *Queue) CancelByRun(ctx context.Context, ownerID string, runID uuid.UUID) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE async_jobs
		SET status = 'cancelled', locked_by = NULL, locked_until = NULL, updated_at = ?
		WHERE owner_id = ? AND run_id = ? AND status IN ('queued','running')`,
		q.now(), ownerID, db.UUIDBytes(runID))
	return err
}

// Release returns a running job to the queue without counting another attempt.
func (q *Queue) Release(ctx context.Context, jobID uuid.UUID) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE async_jobs
		SET status = 'queued', locked_by = NULL, locked_until = NULL, updated_at = ?
		WHERE id = ? AND locked_by = ? AND status = 'running'`, q.now(), db.UUIDBytes(jobID), q.workerID)
	return err
}

func (q *Queue) RequeueExpired(ctx context.Context) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE async_jobs
		SET status = 'queued', locked_by = NULL, locked_until = NULL,
		    last_error = 'worker lock expired', updated_at = ?
		WHERE status = 'running' AND locked_until < ?`, q.now(), q.now())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Backoff is the target architecture's 5s / 30s / 2m retry schedule.
func Backoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
	}
}

func scanClaim(row interface{ Scan(...any) error }) (*Job, error) {
	var job Job
	var rawID []byte
	var runID []byte
	var payload []byte
	if err := row.Scan(&rawID, &job.Kind, &job.OwnerID, &runID, &payload,
		&job.Attempts, &job.MaxAttempts, &job.RunAfter, &job.CreatedAt); err != nil {
		return nil, err
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	job.ID = parsed
	if len(runID) > 0 {
		run, err := db.UUIDFromBytes(runID)
		if err != nil {
			return nil, err
		}
		job.RunID = &run
	}
	job.Payload = payload
	job.Status = StatusQueued
	return &job, nil
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

// QueueAPI is the subset the worker needs; tests use a fake.
type QueueAPI interface {
	Claim(context.Context) (*Job, error)
	Heartbeat(context.Context, uuid.UUID) error
	Complete(context.Context, uuid.UUID) error
	Fail(context.Context, uuid.UUID, error, time.Duration) (bool, error)
	Release(context.Context, uuid.UUID) error
}

type Handler func(context.Context, Job) error

type Worker struct {
	queue             QueueAPI
	handler           Handler
	logger            *slog.Logger
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	RunTimeout        time.Duration
	OnIdle            func()
}

func NewWorker(queue QueueAPI, handler Handler, logger *slog.Logger) *Worker {
	return &Worker{
		queue: queue, handler: handler, logger: logger,
		PollInterval: 500 * time.Millisecond, HeartbeatInterval: 30 * time.Second,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		job, err := w.queue.Claim(ctx)
		if errors.Is(err, ErrNoJob) {
			if w.OnIdle != nil {
				w.OnIdle()
			}
			if !sleep(ctx, w.PollInterval) {
				return nil
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("job_claim_failed", "error", err)
			if !sleep(ctx, w.PollInterval) {
				return nil
			}
			continue
		}
		w.process(ctx, *job)
	}
}

// RunOnce claims and processes at most one job. It returns false when the
// queue is empty; tests and the admin CLI use it instead of starting a loop.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.queue.Claim(ctx)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, w.process(ctx, *job)
}

func (w *Worker) process(ctx context.Context, job Job) error {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(w.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				if err := w.queue.Heartbeat(jobCtx, job.ID); err != nil {
					w.logger.Warn("job_heartbeat_failed", "job_id", job.ID.String(), "error", err)
				}
			}
		}
	}()

	handlerCtx := jobCtx
	if w.RunTimeout > 0 {
		var timeoutCancel context.CancelFunc
		handlerCtx, timeoutCancel = context.WithTimeout(jobCtx, w.RunTimeout)
		defer timeoutCancel()
	}
	err := w.handler(handlerCtx, job)
	parentCancelled := ctx.Err() != nil
	cancel()
	wg.Wait()

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	if err == nil {
		if completeErr := w.queue.Complete(cleanupCtx, job.ID); completeErr != nil {
			w.logger.Warn("job_complete_failed", "job_id", job.ID.String(), "error", completeErr)
			return completeErr
		}
		w.logger.Info("job_succeeded", "job_id", job.ID.String(), "kind", job.Kind)
		return nil
	}
	if parentCancelled {
		// Graceful shutdown: release immediately instead of waiting for backoff.
		if releaseErr := w.queue.Release(cleanupCtx, job.ID); releaseErr != nil {
			w.logger.Warn("job_release_failed", "job_id", job.ID.String(), "error", releaseErr)
		}
		return err
	}
	retry, failErr := w.queue.Fail(cleanupCtx, job.ID, err, 0)
	if failErr != nil {
		w.logger.Error("job_fail_update_failed", "job_id", job.ID.String(), "error", failErr)
		return failErr
	}
	w.logger.Warn("job_failed", "job_id", job.ID.String(), "kind", job.Kind, "retry", retry, "error", err)
	return err
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
