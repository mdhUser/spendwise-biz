package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/identity/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

type Store struct {
	db *sql.DB
}

func New(database *sql.DB) *Store {
	return &Store{db: database}
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, email, email_normalized, password_hash, display_name, status,
		       failed_login_count, locked_until, password_changed_at, last_login_at,
		       created_at, updated_at
		FROM users WHERE email_normalized = ?`, email)
	return scanUser(row)
}

func (s *Store) FindUserByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, email, email_normalized, password_hash, display_name, status,
		       failed_login_count, locked_until, password_changed_at, last_login_at,
		       created_at, updated_at
		FROM users WHERE id = ?`, db.UUIDBytes(id))
	return scanUser(row)
}

func scanUser(row rowScanner) (*domain.User, error) {
	var user domain.User
	var id []byte
	var lockedUntil, lastLogin sql.NullTime
	if err := row.Scan(&id, &user.Email, &user.EmailNormalized, &user.PasswordHash,
		&user.DisplayName, &user.Status, &user.FailedLoginCount, &lockedUntil,
		&user.PasswordChangedAt, &lastLogin, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	parsed, err := db.UUIDFromBytes(id)
	if err != nil {
		return nil, err
	}
	user.ID = parsed
	if lockedUntil.Valid {
		user.LockedUntil = &lockedUntil.Time
	}
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}

func (s *Store) InsertUser(ctx context.Context, q queryer, user *domain.User) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO users (id, email, email_normalized, password_hash, display_name, status,
		                   failed_login_count, password_changed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(user.ID), user.Email, user.EmailNormalized, user.PasswordHash,
		user.DisplayName, user.Status, user.FailedLoginCount, user.PasswordChangedAt,
		user.CreatedAt, user.UpdatedAt)
	return err
}

func (s *Store) InsertSession(ctx context.Context, q queryer, session *domain.Session) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, absolute_expires_at,
		                      created_at, last_seen_at, client_label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(session.ID), db.UUIDBytes(session.UserID), session.TokenHash,
		session.ExpiresAt, session.AbsoluteExpiresAt, session.CreatedAt,
		session.LastSeenAt, session.ClientLabel)
	return err
}

func (s *Store) FindSessionByTokenHash(ctx context.Context, hash []byte) (*domain.User, *domain.Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.email_normalized, u.password_hash, u.display_name, u.status,
		       u.failed_login_count, u.locked_until, u.password_changed_at, u.last_login_at,
		       u.created_at, u.updated_at,
		       s.id, s.user_id, s.token_hash, s.expires_at, s.absolute_expires_at,
		       s.created_at, s.last_seen_at, s.revoked_at, s.client_label
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL`, hash)
	var user domain.User
	var session domain.Session
	var userID, sessionID, sessionUserID []byte
	var lockedUntil, lastLogin, revokedAt sql.NullTime
	if err := row.Scan(&userID, &user.Email, &user.EmailNormalized, &user.PasswordHash,
		&user.DisplayName, &user.Status, &user.FailedLoginCount, &lockedUntil,
		&user.PasswordChangedAt, &lastLogin, &user.CreatedAt, &user.UpdatedAt,
		&sessionID, &sessionUserID, &session.TokenHash, &session.ExpiresAt,
		&session.AbsoluteExpiresAt, &session.CreatedAt, &session.LastSeenAt,
		&revokedAt, &session.ClientLabel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, domain.ErrUnauthenticated
		}
		return nil, nil, err
	}
	var err error
	if user.ID, err = db.UUIDFromBytes(userID); err != nil {
		return nil, nil, err
	}
	if session.ID, err = db.UUIDFromBytes(sessionID); err != nil {
		return nil, nil, err
	}
	if session.UserID, err = db.UUIDFromBytes(sessionUserID); err != nil {
		return nil, nil, err
	}
	if lockedUntil.Valid {
		user.LockedUntil = &lockedUntil.Time
	}
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	if revokedAt.Valid {
		session.RevokedAt = &revokedAt.Time
	}
	return &user, &session, nil
}

func (s *Store) RevokeSession(ctx context.Context, q queryer, sessionID uuid.UUID) error {
	_, err := q.ExecContext(ctx, `UPDATE sessions SET revoked_at = UTC_TIMESTAMP(3) WHERE id = ? AND revoked_at IS NULL`, db.UUIDBytes(sessionID))
	return err
}

func (s *Store) RevokeOtherSessions(ctx context.Context, q queryer, userID, keepID uuid.UUID) error {
	_, err := q.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = UTC_TIMESTAMP(3)
		WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`, db.UUIDBytes(userID), db.UUIDBytes(keepID))
	return err
}

func (s *Store) TouchSession(ctx context.Context, q queryer, sessionID uuid.UUID, expiresAt, lastSeenAt time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE sessions SET expires_at = ?, last_seen_at = ? WHERE id = ?`,
		expiresAt, lastSeenAt, db.UUIDBytes(sessionID))
	return err
}

func (s *Store) UpdateLoginSuccess(ctx context.Context, q queryer, userID uuid.UUID) error {
	_, err := q.ExecContext(ctx, `
		UPDATE users SET failed_login_count = 0, locked_until = NULL,
		                 last_login_at = UTC_TIMESTAMP(3), updated_at = UTC_TIMESTAMP(3)
		WHERE id = ?`, db.UUIDBytes(userID))
	return err
}

func (s *Store) UpdateLoginFailure(ctx context.Context, q queryer, userID uuid.UUID) error {
	_, err := q.ExecContext(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    locked_until = CASE
		        WHEN failed_login_count + 1 >= 20 THEN DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 24 HOUR)
		        WHEN failed_login_count + 1 >= 15 THEN DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)
		        WHEN failed_login_count + 1 >= 10 THEN DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 30 MINUTE)
		        WHEN failed_login_count + 1 >= 5 THEN DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 15 MINUTE)
		        ELSE locked_until
		    END,
		    updated_at = UTC_TIMESTAMP(3)
		WHERE id = ?`, db.UUIDBytes(userID))
	return err
}

func (s *Store) UpdatePassword(ctx context.Context, q queryer, userID uuid.UUID, hash string) error {
	_, err := q.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, password_changed_at = UTC_TIMESTAMP(3),
		                 updated_at = UTC_TIMESTAMP(3)
		WHERE id = ?`, hash, db.UUIDBytes(userID))
	return err
}

func (s *Store) CountActiveSessions(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL
		  AND expires_at > UTC_TIMESTAMP(3) AND absolute_expires_at > UTC_TIMESTAMP(3)`,
		db.UUIDBytes(userID)).Scan(&count)
	return count, err
}

func (s *Store) OldestActiveSession(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL
		  AND expires_at > UTC_TIMESTAMP(3) AND absolute_expires_at > UTC_TIMESTAMP(3)
		ORDER BY last_seen_at ASC LIMIT 1`, db.UUIDBytes(userID)).Scan(&raw)
	if err != nil {
		return uuid.Nil, err
	}
	return db.UUIDFromBytes(raw)
}

func (s *Store) InsertInvite(ctx context.Context, q queryer, invite *domain.Invite) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO invites (id, code_hash, max_uses, uses, expires_at, disabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(invite.ID), invite.CodeHash, invite.MaxUses, invite.Uses,
		invite.ExpiresAt, invite.Disabled, invite.CreatedAt)
	return err
}

func (s *Store) FindInviteForUpdate(ctx context.Context, tx *sql.Tx, codeHash []byte) (*domain.Invite, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, code_hash, max_uses, uses, expires_at, disabled, created_at
		FROM invites WHERE code_hash = ? FOR UPDATE`, codeHash)
	var invite domain.Invite
	var raw []byte
	if err := row.Scan(&raw, &invite.CodeHash, &invite.MaxUses, &invite.Uses,
		&invite.ExpiresAt, &invite.Disabled, &invite.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrInviteInvalid
		}
		return nil, err
	}
	id, err := db.UUIDFromBytes(raw)
	if err != nil {
		return nil, err
	}
	invite.ID = id
	return &invite, nil
}

func (s *Store) IncrementInviteUse(ctx context.Context, tx *sql.Tx, inviteID uuid.UUID) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE invites SET uses = uses + 1
		WHERE id = ? AND disabled = 0 AND expires_at > UTC_TIMESTAMP(3) AND uses < max_uses`,
		db.UUIDBytes(inviteID))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.ErrInviteInvalid
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error {
	// ledger tables are removed by ledger.Store.PurgeUser before this call.
	_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, db.UUIDBytes(userID))
	return err
}

func (s *Store) SessionByID(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, expires_at, absolute_expires_at, created_at,
		       last_seen_at, revoked_at, client_label
		FROM sessions WHERE id = ?`, db.UUIDBytes(id))
	var session domain.Session
	var raw, userRaw []byte
	var revoked sql.NullTime
	if err := row.Scan(&raw, &userRaw, &session.TokenHash, &session.ExpiresAt,
		&session.AbsoluteExpiresAt, &session.CreatedAt, &session.LastSeenAt,
		&revoked, &session.ClientLabel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if session.ID, err = db.UUIDFromBytes(raw); err != nil {
		return nil, err
	}
	if session.UserID, err = db.UUIDFromBytes(userRaw); err != nil {
		return nil, err
	}
	if revoked.Valid {
		session.RevokedAt = &revoked.Time
	}
	return &session, nil
}

// DeleteExpiredSessions removes sessions that expired more than retention ago,
// or that were revoked that long ago. It deletes in batches so a large backlog
// does not hold one long transaction. Rows are already unusable at this point;
// the delay keeps a short audit window before physical removal.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time, retention time.Duration) (int64, error) {
	cutoff := now.UTC().Add(-retention)
	const batchSize = 1000
	var total int64
	for {
		result, err := s.db.ExecContext(ctx, `
			DELETE FROM sessions
			WHERE expires_at < ? OR absolute_expires_at < ?
			   OR (revoked_at IS NOT NULL AND revoked_at < ?)
			LIMIT ?`, cutoff, cutoff, cutoff, batchSize)
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

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Exec(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) TableName() string { return "identity" }
