package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

type TransactionPage struct {
	Items      []domain.Transaction
	NextCursor string
}

type transactionCursor struct {
	Time int64  `json:"t"`
	ID   string `json:"id"`
}

func encodeTransactionCursor(t time.Time, id uuid.UUID) string {
	raw, _ := json.Marshal(transactionCursor{Time: t.UnixMicro(), ID: id.String()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeTransactionCursor(value string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	var cursor transactionCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(cursor.ID)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return time.UnixMicro(cursor.Time).UTC(), id, nil
}

const transactionColumns = `
	id, owner_id, type, amount_fen, occurred_at, category_id, account_id,
	note, merchant, tags, version, created_at, updated_at, deleted_at`

func (s *Store) CreateTransaction(ctx context.Context, q queryer, tx *domain.Transaction) error {
	tags, err := json.Marshal(tx.Tags)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO transactions
		(id, owner_id, type, amount_fen, occurred_at, category_id, account_id,
		 note, merchant, tags, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(tx.ID), db.UUIDBytes(tx.OwnerID), tx.Type, tx.AmountFen,
		tx.OccurredAt, nullableUUID(tx.CategoryID), nullableUUID(tx.AccountID),
		tx.Note, tx.Merchant, tags, tx.Version, tx.CreatedAt, tx.UpdatedAt)
	return err
}

func (s *Store) GetTransaction(ctx context.Context, ownerID, id uuid.UUID) (*domain.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+transactionColumns+`
		FROM transactions WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		db.UUIDBytes(id), db.UUIDBytes(ownerID))
	return scanTransaction(row)
}

func (s *Store) ListTransactions(ctx context.Context, ownerID uuid.UUID, filter domain.TransactionFilter) (TransactionPage, error) {
	query := `SELECT ` + transactionColumns + ` FROM transactions
	          WHERE owner_id = ? AND deleted_at IS NULL`
	args := []any{db.UUIDBytes(ownerID)}
	if filter.From != nil {
		query += ` AND occurred_at >= ?`
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		query += ` AND occurred_at < ?`
		args = append(args, *filter.To)
	}
	if filter.Type != "" {
		query += ` AND type = ?`
		args = append(args, filter.Type)
	}
	if filter.CategoryID != nil {
		query += ` AND category_id = ?`
		args = append(args, db.UUIDBytes(*filter.CategoryID))
	}
	if filter.AccountID != nil {
		query += ` AND account_id = ?`
		args = append(args, db.UUIDBytes(*filter.AccountID))
	}
	if filter.Query != "" {
		query += ` AND (note LIKE ? OR merchant LIKE ?)`
		like := "%" + filter.Query + "%"
		args = append(args, like, like)
	}
	if filter.Cursor != "" {
		at, id, err := decodeTransactionCursor(filter.Cursor)
		if err != nil {
			return TransactionPage{}, fmt.Errorf("%w: invalid cursor", ErrConflict)
		}
		query += ` AND (occurred_at < ? OR (occurred_at = ? AND id < ?))`
		args = append(args, at, at, db.UUIDBytes(id))
	}
	query += ` ORDER BY occurred_at DESC, id DESC LIMIT ?`
	args = append(args, filter.Limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return TransactionPage{}, err
	}
	defer rows.Close()
	var items []domain.Transaction
	for rows.Next() {
		item, err := scanTransaction(rows)
		if err != nil {
			return TransactionPage{}, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return TransactionPage{}, err
	}
	page := TransactionPage{Items: items}
	if len(items) > filter.Limit {
		page.Items = items[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeTransactionCursor(last.OccurredAt, last.ID)
	}
	return page, nil
}

func (s *Store) UpdateTransaction(ctx context.Context, q queryer, tx *domain.Transaction, expectedVersion int) error {
	tags, err := json.Marshal(tx.Tags)
	if err != nil {
		return err
	}
	result, err := q.ExecContext(ctx, `
		UPDATE transactions
		SET type = ?, amount_fen = ?, occurred_at = ?, category_id = ?, account_id = ?,
		    note = ?, merchant = ?, tags = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND version = ? AND deleted_at IS NULL`,
		tx.Type, tx.AmountFen, tx.OccurredAt, nullableUUID(tx.CategoryID),
		nullableUUID(tx.AccountID), tx.Note, tx.Merchant, tags, tx.UpdatedAt,
		db.UUIDBytes(tx.ID), db.UUIDBytes(tx.OwnerID), expectedVersion)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func (s *Store) SoftDeleteTransaction(ctx context.Context, q queryer, ownerID, id uuid.UUID, now time.Time) error {
	result, err := q.ExecContext(ctx, `
		UPDATE transactions SET deleted_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`,
		now, now, db.UUIDBytes(id), db.UUIDBytes(ownerID))
	if err != nil {
		return err
	}
	return expectOne(result)
}

func (s *Store) RestoreTransaction(ctx context.Context, q queryer, ownerID, id uuid.UUID, now time.Time) error {
	result, err := q.ExecContext(ctx, `
		UPDATE transactions SET deleted_at = NULL, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ?
		  AND deleted_at IS NOT NULL
		  AND deleted_at >= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 30 DAY)`,
		now, db.UUIDBytes(id), db.UUIDBytes(ownerID))
	if err != nil {
		return err
	}
	return expectOne(result)
}

func scanTransaction(row interface{ Scan(...any) error }) (*domain.Transaction, error) {
	var tx domain.Transaction
	var id, owner, category, account []byte
	var tags []byte
	var deleted sql.NullTime
	if err := row.Scan(&id, &owner, &tx.Type, &tx.AmountFen, &tx.OccurredAt,
		&category, &account, &tx.Note, &tx.Merchant, &tags, &tx.Version,
		&tx.CreatedAt, &tx.UpdatedAt, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if tx.ID, err = db.UUIDFromBytes(id); err != nil {
		return nil, err
	}
	if tx.OwnerID, err = db.UUIDFromBytes(owner); err != nil {
		return nil, err
	}
	if tx.CategoryID, err = db.UUIDPtrFromBytes(category); err != nil {
		return nil, err
	}
	if tx.AccountID, err = db.UUIDPtrFromBytes(account); err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		if err := json.Unmarshal(tags, &tx.Tags); err != nil {
			return nil, err
		}
	}
	if tx.Tags == nil {
		tx.Tags = []string{}
	}
	if deleted.Valid {
		tx.DeletedAt = &deleted.Time
	}
	return &tx, nil
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return db.UUIDBytes(*id)
}

func (s *Store) MonthlySpend(ctx context.Context, ownerID uuid.UUID, from, to time.Time) ([]domain.MonthlySpend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT EXTRACT(YEAR_MONTH FROM DATE_ADD(occurred_at, INTERVAL 8 HOUR)) AS period_ym,
		       category_id,
		       CAST(SUM(CASE WHEN type = 'expense' THEN amount_fen ELSE -amount_fen END) AS SIGNED) AS net_fen,
		       COUNT(*) AS record_count
		FROM transactions
		WHERE owner_id = ? AND deleted_at IS NULL
		  AND type IN ('expense', 'refund')
		  AND occurred_at >= ? AND occurred_at < ?
		GROUP BY period_ym, category_id
		ORDER BY period_ym ASC`, db.UUIDBytes(ownerID), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MonthlySpend
	for rows.Next() {
		var yearMonth int
		var category []byte
		var net, count int64
		if err := rows.Scan(&yearMonth, &category, &net, &count); err != nil {
			return nil, err
		}
		categoryID, err := db.UUIDPtrFromBytes(category)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MonthlySpend{
			Period:      domain.NewPeriod(yearMonth/100, time.Month(yearMonth%100)),
			CategoryID:  categoryID,
			NetFen:      net,
			RecordCount: count,
		})
	}
	return out, rows.Err()
}

func (s *Store) Summary(ctx context.Context, ownerID uuid.UUID, from, to time.Time, groupBy string) (domain.Summary, error) {
	var summary domain.Summary
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN type = 'income' THEN amount_fen ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'expense' THEN amount_fen ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'refund' THEN amount_fen ELSE 0 END), 0)
		FROM transactions
		WHERE owner_id = ? AND deleted_at IS NULL
		  AND occurred_at >= ? AND occurred_at < ?`,
		db.UUIDBytes(ownerID), from, to).Scan(&summary.IncomeFen, &summary.ExpenseFen, &summary.RefundFen)
	if err != nil {
		return domain.Summary{}, err
	}
	summary.NetSpendFen = summary.ExpenseFen - summary.RefundFen

	var keyExpr string
	switch groupBy {
	case "month":
		keyExpr = "DATE_FORMAT(DATE_ADD(t.occurred_at, INTERVAL 8 HOUR), '%Y-%m')"
	case "day":
		keyExpr = "DATE_FORMAT(DATE_ADD(t.occurred_at, INTERVAL 8 HOUR), '%Y-%m-%d')"
	default:
		keyExpr = "COALESCE(c.name, '未分类')"
	}
	query := fmt.Sprintf(`
		SELECT %s AS group_key, t.category_id,
		       COALESCE(SUM(CASE WHEN t.type = 'expense' THEN t.amount_fen ELSE 0 END), 0) AS expense_fen,
		       COALESCE(SUM(CASE WHEN t.type = 'refund' THEN t.amount_fen ELSE 0 END), 0) AS refund_fen
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.owner_id = t.owner_id
		WHERE t.owner_id = ? AND t.deleted_at IS NULL
		  AND t.occurred_at >= ? AND t.occurred_at < ?
		  AND t.type IN ('expense', 'refund')
		GROUP BY group_key, t.category_id
		ORDER BY group_key ASC`, keyExpr)
	rows, err := s.db.QueryContext(ctx, query, db.UUIDBytes(ownerID), from, to)
	if err != nil {
		return domain.Summary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var group domain.SummaryGroup
		var category []byte
		if err := rows.Scan(&group.Key, &category, &group.ExpenseFen, &group.RefundFen); err != nil {
			return domain.Summary{}, err
		}
		var err error
		if group.CategoryID, err = db.UUIDPtrFromBytes(category); err != nil {
			return domain.Summary{}, err
		}
		group.NetSpendFen = group.ExpenseFen - group.RefundFen
		summary.Groups = append(summary.Groups, group)
	}
	return summary, rows.Err()
}

func (s *Store) HasCategory(ctx context.Context, ownerID, categoryID uuid.UUID) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM categories
		WHERE id = ? AND owner_id = ? AND archived_at IS NULL`,
		db.UUIDBytes(categoryID), db.UUIDBytes(ownerID)).Scan(&count)
	return count == 1, err
}

func (s *Store) HasAccount(ctx context.Context, ownerID, accountID uuid.UUID) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM accounts
		WHERE id = ? AND owner_id = ? AND archived_at IS NULL`,
		db.UUIDBytes(accountID), db.UUIDBytes(ownerID)).Scan(&count)
	return count == 1, err
}

func (s *Store) CategoryKind(ctx context.Context, ownerID, categoryID uuid.UUID) (string, error) {
	var kind string
	err := s.db.QueryRowContext(ctx, `
		SELECT kind FROM categories WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(categoryID), db.UUIDBytes(ownerID)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return kind, err
}
