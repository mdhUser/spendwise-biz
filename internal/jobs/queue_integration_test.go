package jobs

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/migrate"
)

func TestQueueClaimCompleteAndRetry(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(database, "worker-test")
	queue.SetClock(func() time.Time { return now })
	owner := "jobs-test-owner"
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = database.ExecContext(cleanupCtx, `DELETE FROM async_jobs WHERE owner_id = ?`, owner)
	})

	job := &Job{Kind: "run.substitution", OwnerID: owner, Payload: []byte(`{"runId":"x"}`)}
	if err := db.WithTx(ctx, database, nil, func(tx *sql.Tx) error {
		return queue.Enqueue(ctx, tx, job)
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := queue.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.ID != job.ID || claimed.Attempts != 1 || claimed.Status != StatusRunning {
		t.Fatalf("claimed = %+v", claimed)
	}
	if err := queue.Heartbeat(ctx, claimed.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := queue.Complete(ctx, claimed.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := queue.Claim(ctx); err != ErrNoJob {
		t.Fatalf("Claim after complete err = %v, want ErrNoJob", err)
	}

	retryJob := &Job{Kind: "run.substitution", OwnerID: owner, Payload: []byte(`{}`), MaxAttempts: 3}
	if err := db.WithTx(ctx, database, nil, func(tx *sql.Tx) error { return queue.Enqueue(ctx, tx, retryJob) }); err != nil {
		t.Fatal(err)
	}
	first, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := queue.Fail(ctx, first.ID, context.DeadlineExceeded, 0)
	if err != nil || !retry {
		t.Fatalf("Fail retry = %v, err = %v", retry, err)
	}
	if _, err := queue.Claim(ctx); err != ErrNoJob {
		t.Fatalf("claim before backoff err = %v, want ErrNoJob", err)
	}
	now = now.Add(6 * time.Second)
	second, err := queue.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim after backoff: %v", err)
	}
	if second.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", second.Attempts)
	}
}

func TestQueueRequeueExpired(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(database, "worker-expired")
	queue.SetClock(func() time.Time { return now })
	owner := "jobs-expired-owner"
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM async_jobs WHERE owner_id = ?`, owner)
	})

	job := &Job{Kind: "run.substitution", OwnerID: owner, Payload: []byte(`{}`), MaxAttempts: 1}
	if err := db.WithTx(ctx, database, nil, func(tx *sql.Tx) error { return queue.Enqueue(ctx, tx, job) }); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(121 * time.Second)
	count, err := queue.RequeueExpired(ctx)
	if err != nil || count != 1 {
		t.Fatalf("RequeueExpired = %d, %v; want 1", count, err)
	}
	reclaimed, err := queue.Claim(ctx)
	if err != nil || reclaimed.ID != job.ID {
		t.Fatalf("reclaim = %+v, %v", reclaimed, err)
	}
}

type fakeQueue struct {
	job       *Job
	completed bool
	failed    bool
	released  bool
}

func (f *fakeQueue) Claim(context.Context) (*Job, error) { return f.job, nil }
func (f *fakeQueue) Heartbeat(context.Context, uuid.UUID) error {
	return nil
}
func (f *fakeQueue) Complete(context.Context, uuid.UUID) error { f.completed = true; return nil }
func (f *fakeQueue) Fail(context.Context, uuid.UUID, error, time.Duration) (bool, error) {
	f.failed = true
	return true, nil
}
func (f *fakeQueue) Release(context.Context, uuid.UUID) error { f.released = true; return nil }

func TestWorkerCompletesAndFails(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	queue := &fakeQueue{job: &Job{ID: uuid.MustParse("01010101-0101-0101-0101-010101010101")}}
	worker := NewWorker(queue, func(context.Context, Job) error { return nil }, logger)
	if ok, err := worker.RunOnce(context.Background()); !ok || err != nil {
		t.Fatalf("RunOnce = %v, %v", ok, err)
	}
	if !queue.completed {
		t.Fatal("job was not completed")
	}

	queue = &fakeQueue{job: &Job{ID: uuid.MustParse("02020202-0202-0202-0202-020202020202")}}
	worker = NewWorker(queue, func(context.Context, Job) error { return context.DeadlineExceeded }, logger)
	if ok, err := worker.RunOnce(context.Background()); !ok || err == nil {
		t.Fatalf("failing RunOnce = %v, %v", ok, err)
	}
	if !queue.failed {
		t.Fatal("job was not failed")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BIZ_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("BIZ_TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(parsed.DBName), "test") {
		t.Fatalf("refusing database %q: name must contain test", parsed.DBName)
	}
	t.Setenv("BIZ_MYSQL_DSN", dsn)
	t.Setenv("BIZ_MIGRATION_DSN", dsn)
	t.Setenv("BIZ_AUTH_BASE_URL", "http://127.0.0.1:1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrate.Apply(context.Background(), database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}
