package domain

import (
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account locked")
	ErrInviteInvalid      = errors.New("invite invalid")
	ErrEmailTaken         = errors.New("email already registered")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrForbidden          = errors.New("forbidden")
)

type User struct {
	ID                uuid.UUID
	Email             string
	EmailNormalized   string
	PasswordHash      string
	DisplayName       string
	Status            string
	FailedLoginCount  int
	LockedUntil       *time.Time
	PasswordChangedAt time.Time
	LastLoginAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	TokenHash         []byte
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	CreatedAt         time.Time
	LastSeenAt        time.Time
	RevokedAt         *time.Time
	ClientLabel       string
}

type Invite struct {
	ID        uuid.UUID
	CodeHash  []byte
	MaxUses   int
	Uses      int
	ExpiresAt time.Time
	Disabled  bool
	CreatedAt time.Time
}

func NormalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value {
		return "", errors.New("invalid email")
	}
	if len(value) > 254 {
		return "", errors.New("email too long")
	}
	return strings.ToLower(value), nil
}

func ValidPassword(value string) bool {
	return len(value) >= 10 && len(value) <= 128
}
