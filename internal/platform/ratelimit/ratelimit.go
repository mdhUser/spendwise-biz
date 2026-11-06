// Package ratelimit implements a MySQL-backed fixed-window limiter. It uses
// one row per key/window and is safe across api replicas without Redis.
package ratelimit

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

type Limiter struct {
	db  *sql.DB
	now func() time.Time
}

func New(database *sql.DB) *Limiter {
	return &Limiter{db: database, now: func() time.Time { return time.Now().UTC() }}
}

func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

type Decision struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Time
}

func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error) {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	now := l.now()
	seconds := int64(window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	start := now.Unix() - now.Unix()%seconds
	bucketKey := "rl:" + truncate(key, 120) + ":" + strconv.FormatInt(start, 10)
	expiresAt := time.Unix(start, 0).UTC().Add(window)

	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rate_buckets (bucket_key, count, expires_at)
		VALUES (?, 1, ?)
		ON DUPLICATE KEY UPDATE
		  count = IF(expires_at <= ?, 1, count + 1),
		  expires_at = IF(expires_at <= ?, VALUES(expires_at), expires_at)`,
		bucketKey, expiresAt, now, now); err != nil {
		return Decision{}, err
	}
	var count int
	var expires time.Time
	if err := tx.QueryRowContext(ctx, `SELECT count, expires_at FROM rate_buckets WHERE bucket_key = ? FOR UPDATE`, bucketKey).
		Scan(&count, &expires); err != nil {
		return Decision{}, err
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, err
	}
	decision := Decision{Limit: limit, Remaining: limit - count, Reset: expires}
	if count > limit {
		decision.Allowed = false
		decision.Remaining = 0
		return decision, nil
	}
	if decision.Remaining < 0 {
		decision.Remaining = 0
	}
	decision.Allowed = true
	return decision, nil
}

// Cleanup removes expired buckets; cmd/admin and the worker cleanup job call
// it so the table does not grow without bound.
func (l *Limiter) Cleanup(ctx context.Context) (int64, error) {
	if l.db == nil {
		return 0, errors.New("ratelimit: nil db")
	}
	result, err := l.db.ExecContext(ctx, `DELETE FROM rate_buckets WHERE expires_at < UTC_TIMESTAMP(3) LIMIT 5000`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
