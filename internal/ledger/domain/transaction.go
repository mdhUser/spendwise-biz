package domain

import (
	"time"

	"github.com/google/uuid"
)

type TransactionType string

const (
	TransactionExpense TransactionType = "expense"
	TransactionIncome  TransactionType = "income"
	TransactionRefund  TransactionType = "refund"
)

func (t TransactionType) Valid() bool {
	return t == TransactionExpense || t == TransactionIncome || t == TransactionRefund
}

type Transaction struct {
	ID         uuid.UUID
	OwnerID    string
	Type       TransactionType
	AmountFen  int64
	OccurredAt time.Time
	CategoryID *uuid.UUID
	AccountID  *uuid.UUID
	Note       string
	Merchant   string
	Tags       []string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

type TransactionFilter struct {
	From       *time.Time
	To         *time.Time
	Type       TransactionType
	CategoryID *uuid.UUID
	AccountID  *uuid.UUID
	Query      string
	Cursor     string
	Limit      int
}

type MonthlySpend struct {
	Period      Period
	CategoryID  *uuid.UUID
	NetFen      int64
	RecordCount int64
}

type SummaryGroup struct {
	Key         string     `json:"key"`
	CategoryID  *uuid.UUID `json:"categoryId"`
	ExpenseFen  int64      `json:"expenseFen"`
	RefundFen   int64      `json:"refundFen"`
	NetSpendFen int64      `json:"netSpendFen"`
}

type Summary struct {
	IncomeFen   int64          `json:"incomeFen"`
	ExpenseFen  int64          `json:"expenseFen"`
	RefundFen   int64          `json:"refundFen"`
	NetSpendFen int64          `json:"netSpendFen"`
	Groups      []SummaryGroup `json:"groups"`
}
