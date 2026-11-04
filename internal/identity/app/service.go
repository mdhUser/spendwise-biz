package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	"github.com/mdhuser/spendwise-biz/internal/identity/domain"
	"github.com/mdhuser/spendwise-biz/internal/identity/password"
	identitystore "github.com/mdhuser/spendwise-biz/internal/identity/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/identity/token"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Service struct {
	db       *sql.DB
	store    *identitystore.Store
	cfg      *config.Config
	loc      *time.Location
	defaults func(context.Context, *sql.Tx, uuid.UUID) error
}

func New(database *sql.DB, store *identitystore.Store, cfg *config.Config) *Service {
	return &Service{db: database, store: store, cfg: cfg, loc: cfg.Location()}
}

func (s *Service) SetDefaults(fn func(context.Context, *sql.Tx, uuid.UUID) error) {
	s.defaults = fn
}

type AuthResult struct {
	User      *domain.User
	Token     string
	ExpiresAt time.Time
}

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	InviteCode  string
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (*AuthResult, error) {
	emailNormalized, err := domain.NormalizeEmail(input.Email)
	if err != nil {
		return nil, apperr.Validation("邮箱格式不正确")
	}
	if !domain.ValidPassword(input.Password) {
		return nil, apperr.Validation("密码长度必须为 10-128 个字符")
	}
	displayName := input.DisplayName
	if len([]rune(displayName)) > 60 {
		return nil, apperr.Validation("显示名称过长")
	}

	var inviteHash []byte
	if s.cfg.RegistrationMode == "invite" {
		inviteHash = s.inviteHash(input.InviteCode)
	}

	passwordHash, err := password.Hash(input.Password)
	if err != nil {
		return nil, err
	}
	rawToken, tokenHash, err := token.New()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	userID := id.New()
	sessionID := id.New()
	expires := now.Add(s.cfg.SessionTTL)
	absolute := now.Add(s.cfg.AbsoluteTTL)

	user := &domain.User{
		ID:                userID,
		Email:             strings.TrimSpace(input.Email),
		EmailNormalized:   emailNormalized,
		PasswordHash:      passwordHash,
		DisplayName:       displayName,
		Status:            "active",
		PasswordChangedAt: now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	session := &domain.Session{
		ID:                sessionID,
		UserID:            userID,
		TokenHash:         tokenHash,
		ExpiresAt:         expires,
		AbsoluteExpiresAt: absolute,
		CreatedAt:         now,
		LastSeenAt:        now,
	}

	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if s.cfg.RegistrationMode != "invite" {
			return apperr.New(403, "REGISTRATION_CLOSED", "当前不开放注册")
		}
		invite, findErr := s.store.FindInviteForUpdate(ctx, tx, inviteHash)
		if findErr != nil {
			return apperr.New(422, "INVITE_INVALID", "邀请码无效、已过期或名额已满")
		}
		if invite.Disabled || !invite.ExpiresAt.After(now) || invite.Uses >= invite.MaxUses {
			return apperr.New(422, "INVITE_INVALID", "邀请码无效、已过期或名额已满")
		}
		if incErr := s.store.IncrementInviteUse(ctx, tx, invite.ID); incErr != nil {
			return apperr.New(422, "INVITE_INVALID", "邀请码无效、已过期或名额已满")
		}
		if insertErr := s.store.InsertUser(ctx, tx, user); insertErr != nil {
			return insertErr
		}
		if s.defaults != nil {
			if defaultsErr := s.defaults(ctx, tx, user.ID); defaultsErr != nil {
				return defaultsErr
			}
		}
		return s.store.InsertSession(ctx, tx, session)
	})
	if err != nil {
		if db.IsDuplicate(err) {
			return nil, apperr.Conflict("EMAIL_TAKEN", "该邮箱已注册")
		}
		return nil, err
	}
	return &AuthResult{User: user, Token: rawToken, ExpiresAt: expires}, nil
}

func (s *Service) Login(ctx context.Context, email, plainPassword string) (*AuthResult, error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return nil, apperr.New(401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
	}
	user, err := s.store.FindUserByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Unknown email still pays one Argon2 verification so response time
			// cannot be used to enumerate registered addresses.
			password.VerifyDummy(plainPassword)
			return nil, apperr.New(401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
		}
		return nil, err
	}
	now := time.Now().UTC()
	if user.Status != "active" || (user.LockedUntil != nil && user.LockedUntil.After(now)) {
		return nil, apperr.New(423, "ACCOUNT_LOCKED", "账号暂时锁定，请稍后再试")
	}
	if !password.Verify(plainPassword, user.PasswordHash) {
		_ = s.store.UpdateLoginFailure(ctx, s.db, user.ID)
		return nil, apperr.New(401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
	}

	if err := s.store.UpdateLoginSuccess(ctx, s.db, user.ID); err != nil {
		return nil, err
	}
	count, err := s.store.CountActiveSessions(ctx, user.ID)
	if err == nil && count >= 5 {
		if oldest, oldestErr := s.store.OldestActiveSession(ctx, user.ID); oldestErr == nil {
			_ = s.store.RevokeSession(ctx, s.db, oldest)
		}
	}

	rawToken, tokenHash, err := token.New()
	if err != nil {
		return nil, err
	}
	session := &domain.Session{
		ID:                id.New(),
		UserID:            user.ID,
		TokenHash:         tokenHash,
		ExpiresAt:         now.Add(s.cfg.SessionTTL),
		AbsoluteExpiresAt: now.Add(s.cfg.AbsoluteTTL),
		CreatedAt:         now,
		LastSeenAt:        now,
	}
	if err := s.store.InsertSession(ctx, s.db, session); err != nil {
		return nil, err
	}
	return &AuthResult{User: user, Token: rawToken, ExpiresAt: session.ExpiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (*identity.Principal, error) {
	hash := token.Hash(rawToken)
	user, session, err := s.store.FindSessionByTokenHash(ctx, hash)
	if err != nil {
		return nil, apperr.Unauthenticated("登录已失效，请重新登录")
	}
	now := time.Now().UTC()
	if user.Status != "active" || !session.ExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) {
		return nil, apperr.Unauthenticated("登录已失效，请重新登录")
	}
	if now.Sub(session.LastSeenAt) >= time.Hour {
		expires := now.Add(s.cfg.SessionTTL)
		if expires.After(session.AbsoluteExpiresAt) {
			expires = session.AbsoluteExpiresAt
		}
		_ = s.store.TouchSession(ctx, s.db, session.ID, expires, now)
		session.ExpiresAt = expires
		session.LastSeenAt = now
	}
	return &identity.Principal{User: *user, Session: *session}, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	hash := token.Hash(rawToken)
	_, session, err := s.store.FindSessionByTokenHash(ctx, hash)
	if err != nil {
		return nil
	}
	return s.store.RevokeSession(ctx, s.db, session.ID)
}

func (s *Service) ChangePassword(ctx context.Context, principal identity.Principal, current, next string) error {
	user, err := s.store.FindUserByID(ctx, principal.User.ID)
	if err != nil {
		return err
	}
	if !password.Verify(current, user.PasswordHash) {
		_ = s.store.UpdateLoginFailure(ctx, s.db, user.ID)
		return apperr.New(401, "INVALID_CREDENTIALS", "当前密码不正确")
	}
	if !domain.ValidPassword(next) {
		return apperr.Validation("新密码长度必须为 10-128 个字符")
	}
	hash, err := password.Hash(next)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.UpdatePassword(ctx, tx, user.ID, hash); err != nil {
			return err
		}
		return s.store.RevokeOtherSessions(ctx, tx, user.ID, principal.Session.ID)
	})
}

func (s *Service) CreateInvite(ctx context.Context, maxUses int, ttl time.Duration) (string, *domain.Invite, error) {
	if maxUses < 1 || maxUses > 10000 {
		return "", nil, fmt.Errorf("max uses must be between 1 and 10000")
	}
	if ttl <= 0 {
		return "", nil, fmt.Errorf("ttl must be positive")
	}
	code, err := newInviteCode()
	if err != nil {
		return "", nil, err
	}
	invite := &domain.Invite{
		ID:        id.New(),
		CodeHash:  s.inviteHash(code),
		MaxUses:   maxUses,
		Uses:      0,
		ExpiresAt: time.Now().UTC().Add(ttl),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.InsertInvite(ctx, s.db, invite); err != nil {
		return "", nil, err
	}
	return code, invite, nil
}

func (s *Service) inviteHash(code string) []byte {
	mac := hmac.New(sha256.New, []byte(s.cfg.InvitePepper))
	_, _ = mac.Write([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return mac.Sum(nil)
}

func newInviteCode() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	groups := make([]string, 0, 4)
	for i := 0; i < len(encoded); i += 4 {
		end := i + 4
		if end > len(encoded) {
			end = len(encoded)
		}
		groups = append(groups, encoded[i:end])
	}
	return "SW-" + strings.Join(groups, "-"), nil
}

func (s *Service) DeleteAccount(ctx context.Context, principal identity.Principal, plainPassword, confirm string, purge func(context.Context, *sql.Tx, uuid.UUID) error) error {
	if confirm != "DELETE" {
		return apperr.Validation("确认短语必须是 DELETE")
	}
	user, err := s.store.FindUserByID(ctx, principal.User.ID)
	if err != nil {
		return err
	}
	if !password.Verify(plainPassword, user.PasswordHash) {
		_ = s.store.UpdateLoginFailure(ctx, s.db, user.ID)
		return apperr.New(401, "INVALID_CREDENTIALS", "密码不正确")
	}
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if purge != nil {
			if err := purge(ctx, tx, user.ID); err != nil {
				return err
			}
		}
		return s.store.DeleteUser(ctx, tx, user.ID)
	})
}
