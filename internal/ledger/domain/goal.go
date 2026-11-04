package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	GoalActive   = "active"
	GoalArchived = "archived"

	ScopeAll        = "all"
	ScopeCategories = "categories"

	BaselineFixed          = "fixed"
	BaselinePreviousPeriod = "previous_period"

	StatusAchieved     = "achieved"
	StatusMissed       = "missed"
	StatusInProgress   = "in_progress"
	StatusBaselineMiss = "baseline_missing"
	StatusBaselineLow  = "baseline_too_low"
)

type SavingGoal struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	Status      string
	StartPeriod Period
	EndPeriod   *Period
	Version     int
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Scope struct {
	Kind        string      `json:"kind"`
	CategoryIDs []uuid.UUID `json:"categoryIds"`
}

type GoalTerm struct {
	ID                  uuid.UUID
	GoalID              uuid.UUID
	OwnerID             uuid.UUID
	Version             int
	EffectiveFromPeriod Period
	ScopeKind           string
	CategoryIDs         []uuid.UUID
	BaselineKind        string
	BaselineFen         *int64
	InitialBaselineFen  *int64
	TargetSavingFen     int64
	CreatedAt           time.Time
}

func (t GoalTerm) Scope() Scope {
	ids := append([]uuid.UUID{}, t.CategoryIDs...)
	return Scope{Kind: t.ScopeKind, CategoryIDs: ids}
}
