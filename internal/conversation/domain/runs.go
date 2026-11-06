package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrRunCancelled = errors.New("conversation: run cancelled")

const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// SearchRun is one asynchronous substitution/recommendation request.
type SearchRun struct {
	ID                uuid.UUID
	OwnerID           string
	ConversationID    uuid.UUID
	UseCaseRevisionID *uuid.UUID
	SourceSKUID       *uuid.UUID
	SourceQuoteID     *uuid.UUID
	Input             json.RawMessage
	Status            string
	PlatformStatus    json.RawMessage
	Reply             string
	RequestKey        string
	Attempts          int
	Error             string
	StartedAt         time.Time
	FinishedAt        *time.Time
	ExpiresAt         time.Time
	UpdatedAt         time.Time
}

// Assessment is a comparable candidate produced by the worker.
type Assessment struct {
	ID               uuid.UUID
	OwnerID          string
	SearchRunID      uuid.UUID
	SourceQuoteID    *uuid.UUID
	CandidateQuoteID *uuid.UUID
	CandidateSKUID   *uuid.UUID
	RelationKind     string
	Verdict          string
	ConstraintChecks json.RawMessage
	CostBreakdown    json.RawMessage
	Reason           string
	EngineVersion    int
	CreatedAt        time.Time
}
