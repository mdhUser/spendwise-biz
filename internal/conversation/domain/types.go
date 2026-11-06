// Package domain contains conversation entities and rules.
package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound        = errors.New("conversation: not found")
	ErrVersionConflict = errors.New("conversation: version conflict")
)

const (
	MaxTitleRunes   = 120
	MaxContentRunes = 4000
	ConversationTTL = 30 * 24 * time.Hour
)

type Conversation struct {
	ID        uuid.UUID
	OwnerID   string
	Title     string
	Needs     json.RawMessage
	Asks      int
	Version   int
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Message struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	Role           string
	Content        string
	CreatedAt      time.Time
}
