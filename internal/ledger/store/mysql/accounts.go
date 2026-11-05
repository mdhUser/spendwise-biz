package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Store struct {
	db *sql.DB
}

func New(database *sql.DB) *Store {
	return &Store{db: database}
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) ListAccounts(ctx context.Context, ownerID string, includeArchived bool) ([]domain.Account, error) {
	query := `SELECT id, owner_id, name, kind, archived_at, version, created_at, updated_at
	          FROM accounts WHERE owner_id = ?`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *account)
	}
	return out, rows.Err()
}

func (s *Store) GetAccount(ctx context.Context, ownerID string, id uuid.UUID) (*domain.Account, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, name, kind, archived_at, version, created_at, updated_at
		FROM accounts WHERE id = ? AND owner_id = ?`, db.UUIDBytes(id), ownerID)
	return scanAccount(row)
}

func scanAccount(row interface{ Scan(...any) error }) (*domain.Account, error) {
	var account domain.Account
	var id []byte
	var owner string
	var archived sql.NullTime
	if err := row.Scan(&id, &owner, &account.Name, &account.Kind, &archived,
		&account.Version, &account.CreatedAt, &account.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if account.ID, err = db.UUIDFromBytes(id); err != nil {
		return nil, err
	}
	account.OwnerID = owner
	if archived.Valid {
		account.ArchivedAt = &archived.Time
	}
	return &account, nil
}

func (s *Store) CreateAccount(ctx context.Context, q queryer, account *domain.Account) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO accounts (id, owner_id, name, kind, archived_at, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(account.ID), account.OwnerID, account.Name, account.Kind,
		account.ArchivedAt, account.Version, account.CreatedAt, account.UpdatedAt)
	return err
}

func (s *Store) UpdateAccount(ctx context.Context, q queryer, account *domain.Account, expectedVersion int) error {
	result, err := q.ExecContext(ctx, `
		UPDATE accounts SET name = ?, kind = ?, archived_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND version = ?`,
		account.Name, account.Kind, account.ArchivedAt, account.UpdatedAt,
		db.UUIDBytes(account.ID), account.OwnerID, expectedVersion)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func (s *Store) ArchiveAccount(ctx context.Context, q queryer, ownerID string, id uuid.UUID, now time.Time) error {
	result, err := q.ExecContext(ctx, `
		UPDATE accounts SET archived_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND archived_at IS NULL`,
		now, now, db.UUIDBytes(id), ownerID)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func (s *Store) ListCategories(ctx context.Context, ownerID string, kind string, includeArchived bool) ([]domain.Category, error) {
	query := `SELECT id, owner_id, name, kind, icon, color, sort_order, archived_at,
	                 version, created_at, updated_at
	          FROM categories WHERE owner_id = ?`
	args := []any{ownerID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY sort_order ASC, created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Category
	for rows.Next() {
		category, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *category)
	}
	return out, rows.Err()
}

func (s *Store) GetCategory(ctx context.Context, ownerID string, id uuid.UUID) (*domain.Category, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, name, kind, icon, color, sort_order, archived_at,
		       version, created_at, updated_at
		FROM categories WHERE id = ? AND owner_id = ?`, db.UUIDBytes(id), ownerID)
	return scanCategory(row)
}

func scanCategory(row interface{ Scan(...any) error }) (*domain.Category, error) {
	var category domain.Category
	var id []byte
	var owner string
	var archived sql.NullTime
	if err := row.Scan(&id, &owner, &category.Name, &category.Kind, &category.Icon,
		&category.Color, &category.SortOrder, &archived, &category.Version,
		&category.CreatedAt, &category.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if category.ID, err = db.UUIDFromBytes(id); err != nil {
		return nil, err
	}
	category.OwnerID = owner
	if archived.Valid {
		category.ArchivedAt = &archived.Time
	}
	return &category, nil
}

func (s *Store) CreateCategory(ctx context.Context, q queryer, category *domain.Category) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO categories (id, owner_id, name, kind, icon, color, sort_order,
		                        archived_at, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(category.ID), category.OwnerID, category.Name,
		category.Kind, category.Icon, category.Color, category.SortOrder,
		category.ArchivedAt, category.Version, category.CreatedAt, category.UpdatedAt)
	return err
}

func (s *Store) UpdateCategory(ctx context.Context, q queryer, category *domain.Category, expectedVersion int) error {
	result, err := q.ExecContext(ctx, `
		UPDATE categories
		SET name = ?, kind = ?, icon = ?, color = ?, sort_order = ?, archived_at = ?,
		    version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND version = ?`,
		category.Name, category.Kind, category.Icon, category.Color, category.SortOrder,
		category.ArchivedAt, category.UpdatedAt, db.UUIDBytes(category.ID),
		category.OwnerID, expectedVersion)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func (s *Store) ArchiveCategory(ctx context.Context, q queryer, ownerID string, id uuid.UUID, now time.Time) error {
	result, err := q.ExecContext(ctx, `
		UPDATE categories SET archived_at = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND owner_id = ? AND archived_at IS NULL`,
		now, now, db.UUIDBytes(id), ownerID)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func expectOne(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

var ErrConflict = errors.New("conflict")

func (s *Store) EnsureDefaults(ctx context.Context, q queryer, ownerID string, now time.Time) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE owner_id = ?`, ownerID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		defaults := []struct {
			name  string
			kind  string
			order int
		}{
			{"餐饮", "expense", 10}, {"交通", "expense", 20}, {"购物", "expense", 30},
			{"居住", "expense", 40}, {"娱乐", "expense", 50}, {"医疗", "expense", 60},
			{"其他", "expense", 70}, {"工资", "income", 10}, {"其他收入", "income", 20},
		}
		for _, item := range defaults {
			category := &domain.Category{
				ID: id.New(), OwnerID: ownerID, Name: item.name, Kind: item.kind,
				SortOrder: item.order, Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := s.CreateCategory(ctx, q, category); err != nil {
				return fmt.Errorf("create default category: %w", err)
			}
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE owner_id = ?`, ownerID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		accounts := []struct {
			name string
			kind string
		}{{"现金", "cash"}, {"支付宝", "alipay"}, {"微信", "wechat"}}
		for _, item := range accounts {
			account := &domain.Account{
				ID: id.New(), OwnerID: ownerID, Name: item.name, Kind: item.kind,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := s.CreateAccount(ctx, q, account); err != nil {
				return fmt.Errorf("create default account: %w", err)
			}
		}
	}
	return nil
}
