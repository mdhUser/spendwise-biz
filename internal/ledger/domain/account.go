package domain

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID         uuid.UUID
	OwnerID    uuid.UUID
	Name       string
	Kind       string
	ArchivedAt *time.Time
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Category struct {
	ID         uuid.UUID
	OwnerID    uuid.UUID
	Name       string
	Kind       string
	Icon       string
	Color      string
	SortOrder  int
	ArchivedAt *time.Time
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func ValidAccountKind(value string) bool {
	switch value {
	case "cash", "debit_card", "credit_card", "alipay", "wechat", "other":
		return true
	default:
		return false
	}
}

func ValidCategoryKind(value string) bool {
	return value == "expense" || value == "income"
}
