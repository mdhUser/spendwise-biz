package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	ledgerstore "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type CreateTransactionInput struct {
	Type       string     `json:"type"`
	AmountFen  int64      `json:"amountFen"`
	OccurredAt string     `json:"occurredAt"`
	CategoryID *uuid.UUID `json:"categoryId"`
	AccountID  *uuid.UUID `json:"accountId"`
	Note       string     `json:"note"`
	Merchant   string     `json:"merchant"`
	Tags       []string   `json:"tags"`
}

type UpdateTransactionInput struct {
	Type       *string      `json:"type"`
	AmountFen  *int64       `json:"amountFen"`
	OccurredAt *string      `json:"occurredAt"`
	CategoryID OptionalUUID `json:"categoryId"`
	AccountID  OptionalUUID `json:"accountId"`
	Note       *string      `json:"note"`
	Merchant   *string      `json:"merchant"`
	Tags       []string     `json:"tags"`
	Version    int          `json:"version"`
}

// OptionalUUID distinguishes the three JSON states of a PATCH field:
// omitted (Set is false), explicit null (Set is true, Value is nil), and a
// concrete value (Set is true, Value points at the parsed UUID).
type OptionalUUID struct {
	Set   bool
	Value *uuid.UUID
}

func (o *OptionalUUID) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var id uuid.UUID
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("id must be a UUID: %w", err)
	}
	o.Value = &id
	return nil
}

// transactionRefOptions relaxes the archived checks for references that are
// already stored on the transaction. Kind matching is always enforced.
type transactionRefOptions struct {
	allowArchivedCategory bool
	allowArchivedAccount  bool
}

func (s *Service) CreateTransaction(ctx context.Context, ownerID, actorID string, requestID, key string, input CreateTransactionInput) (any, bool, error) {
	tx, err := s.buildTransaction(ctx, ownerID, input, transactionRefOptions{})
	if err != nil {
		return nil, false, err
	}
	tx.CreatedAt = time.Now().UTC()
	tx.UpdatedAt = tx.CreatedAt
	tx.Version = 1
	body, replay, err := s.idempotent(ctx, ownerID, key, "POST /v1/ledger/transactions", input, func(sqlTx *sql.Tx) (any, *uuid.UUID, error) {
		if err := s.store.CreateTransaction(ctx, sqlTx, tx); err != nil {
			return nil, nil, err
		}
		view := s.transactionView(ctx, ownerID, tx)
		if err := s.store.InsertAudit(ctx, sqlTx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "transaction", EntityID: tx.ID, Action: "create",
			ActorID: actorID, RequestID: requestID, After: view,
		}); err != nil {
			return nil, nil, err
		}
		return view, &tx.ID, nil
	})
	return body, replay, err
}

func (s *Service) buildTransaction(ctx context.Context, ownerID string, input CreateTransactionInput, refs transactionRefOptions) (*domain.Transaction, error) {
	txType := domain.TransactionType(input.Type)
	if !txType.Valid() {
		return nil, apperr.Validation("流水类型不合法")
	}
	if !domain.ValidAmount(input.AmountFen) {
		return nil, apperr.Validation("金额必须为 1 到 100000000000 之间的整数分")
	}
	occurred, err := time.Parse(time.RFC3339, input.OccurredAt)
	if err != nil {
		return nil, apperr.Validation("occurredAt 必须是带时区的 RFC3339 时间")
	}
	now := time.Now().UTC()
	if occurred.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || occurred.After(now.Add(24*time.Hour)) {
		return nil, apperr.Validation("occurredAt 超出允许范围")
	}
	if len([]rune(input.Note)) > 500 || len([]rune(input.Merchant)) > 120 {
		return nil, apperr.Validation("备注或商家名称过长")
	}
	tags := make([]string, 0, len(input.Tags))
	seen := map[string]struct{}{}
	for _, tag := range input.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if len([]rune(tag)) > 20 {
			return nil, apperr.Validation("单个标签不能超过 20 个字符")
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > 10 {
		return nil, apperr.Validation("标签不能超过 10 个")
	}
	if input.CategoryID != nil {
		category, err := s.store.GetCategory(ctx, ownerID, *input.CategoryID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, apperr.Validation("分类不存在或已归档")
			}
			return nil, err
		}
		if err := validateCategoryReference(category, txType, refs.allowArchivedCategory); err != nil {
			return nil, err
		}
	}
	if input.AccountID != nil {
		account, err := s.store.GetAccount(ctx, ownerID, *input.AccountID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, apperr.Validation("账户不存在或已归档")
			}
			return nil, err
		}
		if err := validateAccountReference(account, refs.allowArchivedAccount); err != nil {
			return nil, err
		}
	}
	return &domain.Transaction{
		ID: id.New(), OwnerID: ownerID, Type: txType, AmountFen: input.AmountFen,
		OccurredAt: occurred.UTC(), CategoryID: input.CategoryID, AccountID: input.AccountID,
		Note: input.Note, Merchant: input.Merchant, Tags: tags,
	}, nil
}

func (s *Service) GetTransaction(ctx context.Context, ownerID string, transactionID uuid.UUID) (any, error) {
	tx, err := s.store.GetTransaction(ctx, ownerID, transactionID)
	if err != nil {
		return nil, apperr.NotFound("流水不存在")
	}
	return s.transactionView(ctx, ownerID, tx), nil
}

func (s *Service) ListTransactions(ctx context.Context, ownerID string, filter domain.TransactionFilter) (any, error) {
	page, err := s.store.ListTransactions(ctx, ownerID, filter)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, s.transactionView(ctx, ownerID, &page.Items[i]))
	}
	return map[string]any{"items": items, "nextCursor": nullableString(page.NextCursor)}, nil
}

type transactionPatch struct {
	input           CreateTransactionInput
	categoryChanged bool
	accountChanged  bool
}

// mergeTransactionUpdate applies a PATCH document to the current transaction.
// categoryChanged/accountChanged are false when the field was omitted or when
// the supplied value is identical to the stored one, which lets updates keep
// referencing an archived category/account.
func mergeTransactionUpdate(current *domain.Transaction, input UpdateTransactionInput) transactionPatch {
	combined := CreateTransactionInput{
		Type: string(current.Type), AmountFen: current.AmountFen,
		OccurredAt: current.OccurredAt.Format(time.RFC3339),
		CategoryID: current.CategoryID, AccountID: current.AccountID,
		Note: current.Note, Merchant: current.Merchant, Tags: current.Tags,
	}
	if input.Type != nil {
		combined.Type = *input.Type
	}
	if input.AmountFen != nil {
		combined.AmountFen = *input.AmountFen
	}
	if input.OccurredAt != nil {
		combined.OccurredAt = *input.OccurredAt
	}
	if input.CategoryID.Set {
		combined.CategoryID = input.CategoryID.Value
	}
	if input.AccountID.Set {
		combined.AccountID = input.AccountID.Value
	}
	if input.Note != nil {
		combined.Note = *input.Note
	}
	if input.Merchant != nil {
		combined.Merchant = *input.Merchant
	}
	if input.Tags != nil {
		combined.Tags = input.Tags
	}
	return transactionPatch{
		input:           combined,
		categoryChanged: input.CategoryID.Set && !sameUUIDPtr(current.CategoryID, input.CategoryID.Value),
		accountChanged:  input.AccountID.Set && !sameUUIDPtr(current.AccountID, input.AccountID.Value),
	}
}

func sameUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validateCategoryReference(category *domain.Category, txType domain.TransactionType, allowArchived bool) error {
	if category.ArchivedAt != nil && !allowArchived {
		return apperr.Validation("分类不存在或已归档")
	}
	if txType == domain.TransactionIncome && category.Kind != "income" {
		return apperr.Validation("收入流水必须使用收入分类")
	}
	if txType != domain.TransactionIncome && category.Kind != "expense" {
		return apperr.Validation("支出/退款流水必须使用支出分类")
	}
	return nil
}

func validateAccountReference(account *domain.Account, allowArchived bool) error {
	if account.ArchivedAt != nil && !allowArchived {
		return apperr.Validation("账户不存在或已归档")
	}
	return nil
}

func (s *Service) UpdateTransaction(ctx context.Context, ownerID, actorID string, transactionID uuid.UUID, requestID string, input UpdateTransactionInput) (any, error) {
	current, err := s.store.GetTransaction(ctx, ownerID, transactionID)
	if err != nil {
		return nil, apperr.NotFound("流水不存在")
	}
	before := s.transactionView(ctx, ownerID, current)
	patch := mergeTransactionUpdate(current, input)
	rebuilt, err := s.buildTransaction(ctx, ownerID, patch.input, transactionRefOptions{
		allowArchivedCategory: !patch.categoryChanged,
		allowArchivedAccount:  !patch.accountChanged,
	})
	if err != nil {
		return nil, err
	}
	rebuilt.ID = current.ID
	rebuilt.OwnerID = ownerID
	rebuilt.Version = current.Version
	rebuilt.CreatedAt = current.CreatedAt
	rebuilt.UpdatedAt = time.Now().UTC()
	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.UpdateTransaction(ctx, tx, rebuilt, input.Version); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.Conflict("VERSION_CONFLICT", "流水已被修改，请刷新后重试")
			}
			return err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "transaction", EntityID: rebuilt.ID, Action: "update",
			ActorID: actorID, RequestID: requestID, Before: before,
			After: s.transactionView(ctx, ownerID, rebuilt),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rebuilt.Version++
	return s.transactionView(ctx, ownerID, rebuilt), nil
}

func (s *Service) DeleteTransaction(ctx context.Context, ownerID, actorID string, transactionID uuid.UUID, requestID string) error {
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.SoftDeleteTransaction(ctx, tx, ownerID, transactionID, time.Now().UTC()); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				// Deleting an already-deleted or missing row is idempotent from
				// the client's perspective; the design requires 204 on repeat.
				return nil
			}
			return err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "transaction", EntityID: transactionID, Action: "delete",
			ActorID: actorID, RequestID: requestID,
		}); err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) RestoreTransaction(ctx context.Context, ownerID, actorID string, transactionID uuid.UUID, requestID string) error {
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.RestoreTransaction(ctx, tx, ownerID, transactionID, time.Now().UTC()); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.NotFound("流水不存在或已超过可恢复时间")
			}
			return err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "transaction", EntityID: transactionID, Action: "restore",
			ActorID: actorID, RequestID: requestID,
		}); err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) Summary(ctx context.Context, ownerID string, from, to time.Time, groupBy string) (any, error) {
	summary, err := s.store.Summary(ctx, ownerID, from, to, groupBy)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"from": from.In(s.loc).Format("2006-01-02"), "to": to.In(s.loc).Add(-time.Second).Format("2006-01-02"),
		"currency": "CNY", "incomeFen": summary.IncomeFen, "expenseFen": summary.ExpenseFen,
		"refundFen": summary.RefundFen, "netSpendFen": summary.NetSpendFen,
		"groups": summary.Groups,
	}, nil
}

func (s *Service) transactionView(ctx context.Context, ownerID string, tx *domain.Transaction) map[string]any {
	view := map[string]any{
		"id": tx.ID, "type": tx.Type, "amountFen": tx.AmountFen, "currency": "CNY",
		"occurredAt": displayTime(tx.OccurredAt), "note": tx.Note, "merchant": tx.Merchant,
		"tags": tx.Tags, "version": tx.Version,
		"createdAt": displayTime(tx.CreatedAt), "updatedAt": displayTime(tx.UpdatedAt),
		"category": nil, "account": nil,
	}
	if tx.CategoryID != nil {
		if category, err := s.store.GetCategory(ctx, ownerID, *tx.CategoryID); err == nil {
			view["category"] = map[string]any{"id": category.ID, "name": category.Name}
		}
	}
	if tx.AccountID != nil {
		if account, err := s.store.GetAccount(ctx, ownerID, *tx.AccountID); err == nil {
			view["account"] = map[string]any{"id": account.ID, "name": account.Name}
		}
	}
	return view
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
