package app

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
)

func TestUpdateTransactionInputDistinguishesOmittedNullAndValue(t *testing.T) {
	categoryID := uuid.New()
	accountID := uuid.New()
	otherID := uuid.New()
	current := &domain.Transaction{
		ID: uuid.New(), Type: domain.TransactionExpense, AmountFen: 1000,
		OccurredAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
		CategoryID: &categoryID, AccountID: &accountID,
	}

	var omitted UpdateTransactionInput
	if err := json.Unmarshal([]byte(`{"version":3}`), &omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if omitted.CategoryID.Set || omitted.AccountID.Set {
		t.Fatalf("omitted fields marked as set: %+v", omitted)
	}
	omittedPatch := mergeTransactionUpdate(current, omitted)
	if omittedPatch.input.CategoryID == nil || *omittedPatch.input.CategoryID != categoryID {
		t.Fatalf("omitted category = %v, want %s", omittedPatch.input.CategoryID, categoryID)
	}
	if omittedPatch.categoryChanged || omittedPatch.accountChanged {
		t.Fatalf("omitted fields reported as changed: %+v", omittedPatch)
	}

	var cleared UpdateTransactionInput
	if err := json.Unmarshal([]byte(`{"version":3,"categoryId":null,"accountId":null}`), &cleared); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !cleared.CategoryID.Set || cleared.CategoryID.Value != nil || !cleared.AccountID.Set || cleared.AccountID.Value != nil {
		t.Fatalf("explicit null not preserved: %+v", cleared)
	}
	clearedPatch := mergeTransactionUpdate(current, cleared)
	if clearedPatch.input.CategoryID != nil || clearedPatch.input.AccountID != nil {
		t.Fatalf("explicit null did not clear references: %+v", clearedPatch.input)
	}
	if !clearedPatch.categoryChanged || !clearedPatch.accountChanged {
		t.Fatalf("explicit null should count as a change: %+v", clearedPatch)
	}

	var same UpdateTransactionInput
	sameJSON := []byte(`{"version":3,"categoryId":"` + categoryID.String() + `","accountId":"` + accountID.String() + `"}`)
	if err := json.Unmarshal(sameJSON, &same); err != nil {
		t.Fatalf("unmarshal same: %v", err)
	}
	samePatch := mergeTransactionUpdate(current, same)
	if samePatch.categoryChanged || samePatch.accountChanged {
		t.Fatalf("unchanged references reported as changed: %+v", samePatch)
	}

	var changed UpdateTransactionInput
	changedJSON := []byte(`{"version":3,"categoryId":"` + otherID.String() + `","accountId":"` + otherID.String() + `"}`)
	if err := json.Unmarshal(changedJSON, &changed); err != nil {
		t.Fatalf("unmarshal changed: %v", err)
	}
	changedPatch := mergeTransactionUpdate(current, changed)
	if !changedPatch.categoryChanged || !changedPatch.accountChanged {
		t.Fatalf("changed references not detected: %+v", changedPatch)
	}

	empty := &domain.Transaction{ID: uuid.New(), Type: domain.TransactionExpense, AmountFen: 1000,
		OccurredAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	var clearAlreadyEmpty UpdateTransactionInput
	if err := json.Unmarshal([]byte(`{"categoryId":null,"accountId":null}`), &clearAlreadyEmpty); err != nil {
		t.Fatalf("unmarshal null for empty transaction: %v", err)
	}
	emptyPatch := mergeTransactionUpdate(empty, clearAlreadyEmpty)
	if emptyPatch.categoryChanged || emptyPatch.accountChanged {
		t.Fatalf("clearing a nil reference should not count as a change: %+v", emptyPatch)
	}
}

func TestUpdateTransactionInputRejectsInvalidUUID(t *testing.T) {
	var input UpdateTransactionInput
	if err := json.Unmarshal([]byte(`{"categoryId":"not-a-uuid"}`), &input); err == nil {
		t.Fatal("expected invalid categoryId to fail decoding")
	}
}

func TestValidateCategoryReferenceAllowsArchivedOnlyWhenUnchanged(t *testing.T) {
	archived := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	expense := &domain.Category{Kind: "expense", ArchivedAt: &archived}

	if err := validateCategoryReference(expense, domain.TransactionExpense, true); err != nil {
		t.Fatalf("archived unchanged category should be accepted: %v", err)
	}
	if err := validateCategoryReference(expense, domain.TransactionExpense, false); err == nil {
		t.Fatal("archived changed category should be rejected")
	} else {
		requireValidationError(t, err)
	}
	if err := validateCategoryReference(expense, domain.TransactionIncome, true); err == nil {
		t.Fatal("kind mismatch must still be rejected for an unchanged archived category")
	} else {
		requireValidationError(t, err)
	}
	if err := validateCategoryReference(&domain.Category{Kind: "income"}, domain.TransactionIncome, false); err != nil {
		t.Fatalf("matching non-archived category should be accepted: %v", err)
	}
	if err := validateCategoryReference(&domain.Category{Kind: "expense"}, domain.TransactionRefund, false); err != nil {
		t.Fatalf("expense category should be valid for refund: %v", err)
	}
}

func TestValidateAccountReferenceAllowsArchivedOnlyWhenUnchanged(t *testing.T) {
	archived := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	account := &domain.Account{Kind: "cash", ArchivedAt: &archived}
	if err := validateAccountReference(account, true); err != nil {
		t.Fatalf("archived unchanged account should be accepted: %v", err)
	}
	if err := validateAccountReference(account, false); err == nil {
		t.Fatal("archived changed account should be rejected")
	} else {
		requireValidationError(t, err)
	}
	if err := validateAccountReference(&domain.Account{Kind: "cash"}, false); err != nil {
		t.Fatalf("non-archived account should be accepted: %v", err)
	}
}

func requireValidationError(t *testing.T, err error) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error %v is not an apperr", err)
	}
	if appErr.Status != 422 || appErr.Code != "VALIDATION_FAILED" {
		t.Fatalf("error = %d %s, want 422 VALIDATION_FAILED", appErr.Status, appErr.Code)
	}
}
