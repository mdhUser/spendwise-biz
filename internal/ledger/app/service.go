package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	ledgerstore "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Service struct {
	db    *sql.DB
	store *ledgerstore.Store
	cfg   *config.Config
	loc   *time.Location
}

func New(database *sql.DB, store *ledgerstore.Store, cfg *config.Config) *Service {
	return &Service{db: database, store: store, cfg: cfg, loc: cfg.Location()}
}

func (s *Service) EnsureDefaults(ctx context.Context, ownerID uuid.UUID) error {
	return s.store.EnsureDefaults(ctx, ownerID, time.Now().UTC())
}

func (s *Service) PurgeUser(ctx context.Context, tx *sql.Tx, ownerID uuid.UUID) error {
	return s.store.PurgeUser(ctx, tx, ownerID)
}

func (s *Service) idempotent(ctx context.Context, ownerID uuid.UUID, key, scope string, payload any, fn func(*sql.Tx) (any, *uuid.UUID, error)) (any, bool, error) {
	if len(key) < 8 || len(key) > 128 {
		return nil, false, apperr.New(400, "IDEMPOTENCY_KEY_REQUIRED", "必须提供 8-128 字符的 Idempotency-Key")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	digest := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	var result any
	replay := false
	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		existing, isReplay, claimErr := s.store.ClaimIdempotency(ctx, tx, ownerID, key, digest[:], s.cfg.IdempotencyTTL)
		if claimErr != nil {
			if errors.Is(claimErr, ledgerstore.ErrIdempotencyInProgress) {
				return apperr.New(409, "IDEMPOTENCY_IN_PROGRESS", "相同请求正在处理中，请稍后重试")
			}
			if errors.Is(claimErr, ledgerstore.ErrIdempotencyMismatch) {
				return apperr.New(409, "IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
			}
			return claimErr
		}
		if isReplay {
			replay = true
			if err := json.Unmarshal(existing.Body, &result); err != nil {
				return err
			}
			return nil
		}
		body, resourceID, fnErr := fn(tx)
		if fnErr != nil {
			return fnErr
		}
		if err := s.store.CompleteIdempotency(ctx, tx, ownerID, key, 201, body, resourceID); err != nil {
			return err
		}
		result = body
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, replay, nil
}

// ---- accounts ----

type CreateAccountInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type UpdateAccountInput struct {
	Name     *string `json:"name"`
	Kind     *string `json:"kind"`
	Archived *bool   `json:"archived"`
	Version  int     `json:"version"`
}

func (s *Service) ListAccounts(ctx context.Context, ownerID uuid.UUID, includeArchived bool) (any, error) {
	items, err := s.store.ListAccounts(ctx, ownerID, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	for i := range items {
		out = append(out, accountView(&items[i]))
	}
	return out, nil
}

func (s *Service) CreateAccount(ctx context.Context, ownerID, actorID uuid.UUID, requestID, key string, input CreateAccountInput) (any, bool, error) {
	name := strings.TrimSpace(input.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 40 {
		return nil, false, apperr.Validation("账户名称长度必须在 1-40 个字符之间")
	}
	if !domain.ValidAccountKind(input.Kind) {
		return nil, false, apperr.Validation("账户类型不合法")
	}
	now := time.Now().UTC()
	account := &domain.Account{
		ID: id.New(), OwnerID: ownerID, Name: name, Kind: input.Kind,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	body, replay, err := s.idempotent(ctx, ownerID, key, "POST /v1/ledger/accounts", input, func(tx *sql.Tx) (any, *uuid.UUID, error) {
		if err := s.store.CreateAccount(ctx, tx, account); err != nil {
			if db.IsDuplicate(err) {
				return nil, nil, apperr.Conflict("CONFLICT", "同名账户已存在")
			}
			return nil, nil, err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "account", EntityID: account.ID, Action: "create",
			ActorID: actorID, RequestID: requestID, After: accountView(account),
		})
		return accountView(account), &account.ID, nil
	})
	return body, replay, err
}

func (s *Service) UpdateAccount(ctx context.Context, ownerID, actorID, accountID uuid.UUID, requestID string, input UpdateAccountInput) (any, error) {
	account, err := s.store.GetAccount(ctx, ownerID, accountID)
	if err != nil {
		return nil, apperr.NotFound("账户不存在")
	}
	before := accountView(account)
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 40 {
			return nil, apperr.Validation("账户名称长度必须在 1-40 个字符之间")
		}
		account.Name = name
	}
	if input.Kind != nil {
		if !domain.ValidAccountKind(*input.Kind) {
			return nil, apperr.Validation("账户类型不合法")
		}
		account.Kind = *input.Kind
	}
	now := time.Now().UTC()
	if input.Archived != nil {
		if *input.Archived {
			if account.ArchivedAt == nil {
				account.ArchivedAt = &now
			}
		} else {
			account.ArchivedAt = nil
		}
	}
	account.UpdatedAt = now
	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.UpdateAccount(ctx, tx, account, input.Version); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.Conflict("VERSION_CONFLICT", "账户已被修改，请刷新后重试")
			}
			if db.IsDuplicate(err) {
				return apperr.Conflict("CONFLICT", "同名账户已存在")
			}
			return err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "account", EntityID: account.ID, Action: "update",
			ActorID: actorID, RequestID: requestID, Before: before, After: accountView(account),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	account.Version++
	return accountView(account), nil
}

func (s *Service) ArchiveAccount(ctx context.Context, ownerID, actorID, accountID uuid.UUID, requestID string) error {
	now := time.Now().UTC()
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.ArchiveAccount(ctx, tx, ownerID, accountID, now); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.NotFound("账户不存在或已归档")
			}
			if db.IsForeignKey(err) {
				return apperr.Conflict("CONFLICT", "账户仍被流水引用，暂不能归档")
			}
			return err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "account", EntityID: accountID, Action: "archive",
			ActorID: actorID, RequestID: requestID,
		})
		return nil
	})
}

func accountView(account *domain.Account) map[string]any {
	return map[string]any{
		"id": account.ID, "name": account.Name, "kind": account.Kind,
		"archivedAt": displayTimePtr(account.ArchivedAt), "version": account.Version,
		"createdAt": displayTime(account.CreatedAt), "updatedAt": displayTime(account.UpdatedAt),
	}
}

// ---- categories ----

type CreateCategoryInput struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Icon      string `json:"icon"`
	Color     string `json:"color"`
	SortOrder int    `json:"sortOrder"`
}

type UpdateCategoryInput struct {
	Name      *string `json:"name"`
	Kind      *string `json:"kind"`
	Icon      *string `json:"icon"`
	Color     *string `json:"color"`
	SortOrder *int    `json:"sortOrder"`
	Archived  *bool   `json:"archived"`
	Version   int     `json:"version"`
}

func (s *Service) ListCategories(ctx context.Context, ownerID uuid.UUID, kind string, includeArchived bool) (any, error) {
	items, err := s.store.ListCategories(ctx, ownerID, kind, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	for i := range items {
		out = append(out, categoryView(&items[i]))
	}
	return out, nil
}

func (s *Service) CreateCategory(ctx context.Context, ownerID, actorID uuid.UUID, requestID, key string, input CreateCategoryInput) (any, bool, error) {
	name := strings.TrimSpace(input.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 40 {
		return nil, false, apperr.Validation("分类名称长度必须在 1-40 个字符之间")
	}
	if !domain.ValidCategoryKind(input.Kind) {
		return nil, false, apperr.Validation("分类类型不合法")
	}
	if input.Color != "" && !validColor(input.Color) {
		return nil, false, apperr.Validation("颜色必须为空或 #RRGGBB")
	}
	now := time.Now().UTC()
	category := &domain.Category{
		ID: id.New(), OwnerID: ownerID, Name: name, Kind: input.Kind,
		Icon: input.Icon, Color: input.Color, SortOrder: input.SortOrder,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	body, replay, err := s.idempotent(ctx, ownerID, key, "POST /v1/ledger/categories", input, func(tx *sql.Tx) (any, *uuid.UUID, error) {
		if err := s.store.CreateCategory(ctx, tx, category); err != nil {
			if db.IsDuplicate(err) {
				return nil, nil, apperr.Conflict("CONFLICT", "同类型同名分类已存在")
			}
			return nil, nil, err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "category", EntityID: category.ID, Action: "create",
			ActorID: actorID, RequestID: requestID, After: categoryView(category),
		})
		return categoryView(category), &category.ID, nil
	})
	return body, replay, err
}

func (s *Service) UpdateCategory(ctx context.Context, ownerID, actorID, categoryID uuid.UUID, requestID string, input UpdateCategoryInput) (any, error) {
	category, err := s.store.GetCategory(ctx, ownerID, categoryID)
	if err != nil {
		return nil, apperr.NotFound("分类不存在")
	}
	before := categoryView(category)
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 40 {
			return nil, apperr.Validation("分类名称长度必须在 1-40 个字符之间")
		}
		category.Name = name
	}
	if input.Kind != nil {
		if !domain.ValidCategoryKind(*input.Kind) {
			return nil, apperr.Validation("分类类型不合法")
		}
		category.Kind = *input.Kind
	}
	if input.Icon != nil {
		category.Icon = *input.Icon
	}
	if input.Color != nil {
		if *input.Color != "" && !validColor(*input.Color) {
			return nil, apperr.Validation("颜色必须为空或 #RRGGBB")
		}
		category.Color = *input.Color
	}
	if input.SortOrder != nil {
		category.SortOrder = *input.SortOrder
	}
	now := time.Now().UTC()
	if input.Archived != nil {
		if *input.Archived {
			if category.ArchivedAt == nil {
				category.ArchivedAt = &now
			}
		} else {
			category.ArchivedAt = nil
		}
	}
	category.UpdatedAt = now
	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.UpdateCategory(ctx, tx, category, input.Version); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.Conflict("VERSION_CONFLICT", "分类已被修改，请刷新后重试")
			}
			if db.IsDuplicate(err) {
				return apperr.Conflict("CONFLICT", "同类型同名分类已存在")
			}
			return err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "category", EntityID: category.ID, Action: "update",
			ActorID: actorID, RequestID: requestID, Before: before, After: categoryView(category),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	category.Version++
	return categoryView(category), nil
}

func (s *Service) ArchiveCategory(ctx context.Context, ownerID, actorID, categoryID uuid.UUID, requestID string) error {
	now := time.Now().UTC()
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.ArchiveCategory(ctx, tx, ownerID, categoryID, now); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.NotFound("分类不存在或已归档")
			}
			if db.IsForeignKey(err) {
				return apperr.Conflict("CONFLICT", "分类仍被流水或目标引用，暂不能归档")
			}
			return err
		}
		_ = s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "category", EntityID: categoryID, Action: "archive",
			ActorID: actorID, RequestID: requestID,
		})
		return nil
	})
}

func categoryView(category *domain.Category) map[string]any {
	return map[string]any{
		"id": category.ID, "name": category.Name, "kind": category.Kind,
		"icon": category.Icon, "color": category.Color, "sortOrder": category.SortOrder,
		"archivedAt": displayTimePtr(category.ArchivedAt), "version": category.Version,
		"createdAt": displayTime(category.CreatedAt), "updatedAt": displayTime(category.UpdatedAt),
	}
}

func validColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
