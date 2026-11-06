package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/catalog/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

type Store struct {
	db *sql.DB
}

func New(database *sql.DB) *Store { return &Store{db: database} }

// ---- categories ----

func (s *Store) ListCategories(ctx context.Context, ownerID string) ([]domain.Category, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_id, name, sort_order, version, created_at, updated_at
		FROM catalog_user_categories WHERE owner_id = ?
		ORDER BY sort_order ASC, id ASC`, ownerID)
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

func (s *Store) GetCategory(ctx context.Context, ownerID string, categoryID uuid.UUID) (*domain.Category, error) {
	return scanCategory(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, name, sort_order, version, created_at, updated_at
		FROM catalog_user_categories WHERE id = ? AND owner_id = ?`, db.UUIDBytes(categoryID), ownerID))
}

func (s *Store) GetCategoryByCreateKey(ctx context.Context, ownerID, key string) (*domain.Category, string, error) {
	var hash string
	category, err := scanCategoryWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, name, sort_order, version, created_at, updated_at, COALESCE(request_hash, '')
		FROM catalog_user_categories WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	return category, hash, err
}

func (s *Store) CountCategories(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_user_categories WHERE owner_id = ?`, ownerID).Scan(&count)
	return count, err
}

func (s *Store) CreateCategory(ctx context.Context, category domain.Category, createKey, requestHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_user_categories
		  (id, owner_id, name, normalized_name, sort_order, create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(category.ID), category.OwnerID, category.Name,
		strings.ToLower(strings.TrimSpace(category.Name)), category.SortOrder,
		nullableString(createKey), nullableString(requestHash))
	return err
}

func (s *Store) UpdateCategory(ctx context.Context, ownerID string, categoryID uuid.UUID, name, normalizedName string, version int) (*domain.Category, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE catalog_user_categories
		SET name = ?, normalized_name = ?, version = version + 1, updated_at = UTC_TIMESTAMP(3)
		WHERE id = ? AND owner_id = ? AND version = ?`,
		name, normalizedName, db.UUIDBytes(categoryID), ownerID, version)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, s.classifyUpdateMiss(ctx, "catalog_user_categories", ownerID, categoryID)
	}
	return s.GetCategory(ctx, ownerID, categoryID)
}

// DeleteCategory detaches products first (no ON DELETE SET NULL with a
// composite owner FK) and deletes the category in one transaction.
func (s *Store) DeleteCategory(ctx context.Context, ownerID string, categoryID uuid.UUID) error {
	return db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE catalog_user_products
			SET category_id = NULL, version = version + 1, updated_at = UTC_TIMESTAMP(3)
			WHERE owner_id = ? AND category_id = ?`, ownerID, db.UUIDBytes(categoryID)); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM catalog_user_categories WHERE id = ? AND owner_id = ?`, db.UUIDBytes(categoryID), ownerID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// ---- products ----

func (s *Store) ListProducts(ctx context.Context, ownerID string, cursor *uuid.UUID, categoryID *uuid.UUID, limit int) ([]domain.Product, error) {
	query := `SELECT id, owner_id, category_id, title, collection_state, version, expires_at, created_at, updated_at
	          FROM catalog_user_products
	          WHERE owner_id = ? AND expires_at > UTC_TIMESTAMP(3)`
	args := []any{ownerID}
	if categoryID != nil {
		query += ` AND category_id = ?`
		args = append(args, db.UUIDBytes(*categoryID))
	}
	if cursor != nil {
		query += ` AND id > ?`
		args = append(args, db.UUIDBytes(*cursor))
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Product
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *product)
	}
	return out, rows.Err()
}

func (s *Store) GetProduct(ctx context.Context, ownerID string, productID uuid.UUID) (*domain.Product, error) {
	return scanProduct(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, category_id, title, collection_state, version, expires_at, created_at, updated_at
		FROM catalog_user_products WHERE id = ? AND owner_id = ? AND expires_at > UTC_TIMESTAMP(3)`,
		db.UUIDBytes(productID), ownerID))
}

func (s *Store) GetProductAnyState(ctx context.Context, ownerID string, productID uuid.UUID) (*domain.Product, error) {
	return scanProduct(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, category_id, title, collection_state, version, expires_at, created_at, updated_at
		FROM catalog_user_products WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(productID), ownerID))
}

func (s *Store) GetProductByCreateKey(ctx context.Context, ownerID, key string) (*domain.Product, string, error) {
	var hash string
	product, err := scanProductWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, category_id, title, collection_state, version, expires_at, created_at, updated_at, COALESCE(request_hash, '')
		FROM catalog_user_products WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	return product, hash, err
}

func (s *Store) CountProducts(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_user_products WHERE owner_id = ?`, ownerID).Scan(&count)
	return count, err
}

func (s *Store) CreateProduct(ctx context.Context, product domain.Product, createKey, requestHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_user_products
		  (id, owner_id, category_id, title, collection_state, expires_at, create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(product.ID), product.OwnerID, nullableUUID(product.CategoryID), product.Title,
		product.CollectionState, product.ExpiresAt, nullableString(createKey), nullableString(requestHash))
	return err
}

func (s *Store) UpdateProduct(ctx context.Context, ownerID string, productID uuid.UUID, title string, categoryID *uuid.UUID, version int) (*domain.Product, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE catalog_user_products
		SET title = ?, category_id = ?, version = version + 1, updated_at = UTC_TIMESTAMP(3)
		WHERE id = ? AND owner_id = ? AND version = ? AND expires_at > UTC_TIMESTAMP(3)`,
		title, nullableUUID(categoryID), db.UUIDBytes(productID), ownerID, version)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, s.classifyUpdateMiss(ctx, "catalog_user_products", ownerID, productID)
	}
	return s.GetProduct(ctx, ownerID, productID)
}

func (s *Store) DeleteProduct(ctx context.Context, ownerID string, productID uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM catalog_user_products WHERE id = ? AND owner_id = ?`, db.UUIDBytes(productID), ownerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ---- SKUs ----

func (s *Store) ListSKUs(ctx context.Context, ownerID string, productID uuid.UUID) ([]domain.SKU, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_id, product_id, display_name, attributes, attribute_schema_version,
		       resolution_state, version, created_at, updated_at
		FROM catalog_user_skus WHERE owner_id = ? AND product_id = ?
		ORDER BY created_at ASC, id ASC`, ownerID, db.UUIDBytes(productID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SKU
	for rows.Next() {
		sku, err := scanSKU(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sku)
	}
	return out, rows.Err()
}

// ListSKUsForProducts batches the library view instead of issuing one query
// per product (target architecture §8.2, no N+1).
func (s *Store) ListSKUsForProducts(ctx context.Context, ownerID string, productIDs []uuid.UUID) ([]domain.SKU, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(productIDs)), ",")
	args := []any{ownerID}
	for _, id := range productIDs {
		args = append(args, db.UUIDBytes(id))
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_id, product_id, display_name, attributes, attribute_schema_version,
		       resolution_state, version, created_at, updated_at
		FROM catalog_user_skus WHERE owner_id = ? AND product_id IN (`+placeholders+`)
		ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SKU
	for rows.Next() {
		sku, err := scanSKU(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sku)
	}
	return out, rows.Err()
}

func (s *Store) GetSKU(ctx context.Context, ownerID string, skuID uuid.UUID) (*domain.SKU, error) {
	return scanSKU(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, product_id, display_name, attributes, attribute_schema_version,
		       resolution_state, version, created_at, updated_at
		FROM catalog_user_skus WHERE id = ? AND owner_id = ?`, db.UUIDBytes(skuID), ownerID))
}

func (s *Store) GetSKUByCreateKey(ctx context.Context, ownerID, key string) (*domain.SKU, *domain.Quote, string, error) {
	var hash string
	sku, err := scanSKUWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, product_id, display_name, attributes, attribute_schema_version,
		       resolution_state, version, created_at, updated_at, COALESCE(request_hash, '')
		FROM catalog_user_skus WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	if err != nil {
		return nil, nil, "", err
	}
	quotes, err := s.ListQuotesForSKU(ctx, ownerID, sku.ID)
	if err != nil {
		return nil, nil, "", err
	}
	var quote *domain.Quote
	if len(quotes) > 0 {
		quote = &quotes[0]
	}
	return sku, quote, hash, nil
}

func (s *Store) CountSKUs(ctx context.Context, ownerID string, productID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM catalog_user_skus WHERE owner_id = ? AND product_id = ?`,
		ownerID, db.UUIDBytes(productID)).Scan(&count)
	return count, err
}

// CreateSKU inserts the SKU and optional initial quote in one transaction.
func (s *Store) CreateSKU(ctx context.Context, sku domain.SKU, createKey, requestHash string, quote *domain.Quote) (*domain.Quote, error) {
	var created *domain.Quote
	err := db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO catalog_user_skus
			  (id, owner_id, product_id, display_name, attributes, attribute_schema_version,
			   resolution_state, create_key, request_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			db.UUIDBytes(sku.ID), sku.OwnerID, db.UUIDBytes(sku.ProductID), sku.DisplayName,
			string(sku.Attributes), sku.SchemaVersion, sku.ResolutionState,
			nullableString(createKey), nullableString(requestHash)); err != nil {
			return err
		}
		if quote != nil {
			quote.SKUID = sku.ID
			if err := insertQuote(ctx, tx, *quote); err != nil {
				return err
			}
			created = quote
		}
		return nil
	})
	return created, err
}

func (s *Store) UpdateSKU(ctx context.Context, ownerID string, skuID uuid.UUID, displayName string, attributes []byte, version int) (*domain.SKU, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE catalog_user_skus
		SET display_name = ?, attributes = ?, version = version + 1, updated_at = UTC_TIMESTAMP(3)
		WHERE id = ? AND owner_id = ? AND version = ?`,
		displayName, string(attributes), db.UUIDBytes(skuID), ownerID, version)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, s.classifyUpdateMiss(ctx, "catalog_user_skus", ownerID, skuID)
	}
	return s.GetSKU(ctx, ownerID, skuID)
}

func (s *Store) DeleteSKU(ctx context.Context, ownerID string, skuID uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM catalog_user_skus WHERE id = ? AND owner_id = ?`, db.UUIDBytes(skuID), ownerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListRecentSKUsWithProduct returns recent SKUs joined to their product title
// for auto-candidate selection. It stays inside the catalog module.
func (s *Store) ListRecentSKUsWithProduct(ctx context.Context, ownerID string, exclude uuid.UUID, limit int) ([]domain.SKUWithProduct, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.owner_id, s.product_id, s.display_name, s.attributes, s.attribute_schema_version,
		       s.resolution_state, s.version, s.created_at, s.updated_at, p.title
		FROM catalog_user_skus s
		JOIN catalog_user_products p ON p.id = s.product_id AND p.owner_id = s.owner_id
		WHERE s.owner_id = ? AND s.id <> ? AND p.expires_at > UTC_TIMESTAMP(3)
		ORDER BY s.created_at DESC, s.id DESC LIMIT ?`, ownerID, db.UUIDBytes(exclude), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SKUWithProduct
	for rows.Next() {
		var sku domain.SKU
		var rawID, rawProduct []byte
		var attributes []byte
		var productTitle string
		if err := rows.Scan(&rawID, &sku.OwnerID, &rawProduct, &sku.DisplayName, &attributes,
			&sku.SchemaVersion, &sku.ResolutionState, &sku.Version, &sku.CreatedAt, &sku.UpdatedAt, &productTitle); err != nil {
			return nil, err
		}
		parsed, err := db.UUIDFromBytes(rawID)
		if err != nil {
			return nil, err
		}
		productID, err := db.UUIDFromBytes(rawProduct)
		if err != nil {
			return nil, err
		}
		sku.ID = parsed
		sku.ProductID = productID
		sku.Attributes = attributes
		out = append(out, domain.SKUWithProduct{SKU: sku, ProductTitle: productTitle})
	}
	return out, rows.Err()
}

// ---- quotes ----

func (s *Store) CreateQuote(ctx context.Context, quote domain.Quote, createKey, requestHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_quote_snapshots
		  (id, owner_id, sku_id, label_snapshot, source_kind, price_kind, price_fen,
		   shipping_fen, currency, stock, source_url, specs_snapshot, discounts_snapshot,
		   region_context, eligibility_context, observed_at, expires_at, schema_version,
		   create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(quote.ID), quote.OwnerID, db.UUIDBytes(quote.SKUID), string(quote.LabelSnapshot),
		quote.SourceKind, quote.PriceKind, nullableInt64(quote.PriceFen), nullableInt64(quote.ShippingFen),
		quote.Currency, quote.Stock, nullableString(quote.SourceURL), string(quote.SpecsSnapshot), string(quote.Discounts),
		string(quote.RegionContext), string(quote.Eligibility), quote.ObservedAt, quote.ExpiresAt,
		quote.SchemaVersion, nullableString(createKey), nullableString(requestHash))
	return err
}

func (s *Store) GetQuoteByCreateKey(ctx context.Context, ownerID, key string) (*domain.Quote, string, error) {
	var hash string
	quote, err := scanQuoteWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, sku_id, label_snapshot, source_kind, price_kind, price_fen,
		       shipping_fen, currency, stock, source_url, specs_snapshot, discounts_snapshot,
		       region_context, eligibility_context, observed_at, expires_at, schema_version,
		       COALESCE(request_hash, '')
		FROM catalog_quote_snapshots WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	return quote, hash, err
}

func (s *Store) ListQuotesForSKU(ctx context.Context, ownerID string, skuID uuid.UUID) ([]domain.Quote, error) {
	rows, err := s.db.QueryContext(ctx, quoteSelect+`
		WHERE owner_id = ? AND sku_id = ?
		ORDER BY observed_at DESC, id DESC`, ownerID, db.UUIDBytes(skuID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Quote
	for rows.Next() {
		quote, err := scanQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *quote)
	}
	return out, rows.Err()
}

func (s *Store) LatestQuotes(ctx context.Context, ownerID string, skuIDs []uuid.UUID) ([]domain.Quote, error) {
	if len(skuIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(skuIDs)), ",")
	args := []any{ownerID}
	for _, id := range skuIDs {
		args = append(args, db.UUIDBytes(id))
	}
	query := `SELECT id, owner_id, sku_id, label_snapshot, source_kind, price_kind, price_fen,
	       shipping_fen, currency, stock, source_url, specs_snapshot, discounts_snapshot,
	       region_context, eligibility_context, observed_at, expires_at, schema_version
		FROM (
		  SELECT q.*, ROW_NUMBER() OVER (PARTITION BY sku_id ORDER BY observed_at DESC, id DESC) AS rn
		  FROM catalog_quote_snapshots q
		  WHERE q.owner_id = ? AND q.sku_id IN (` + placeholders + `)
		) ranked WHERE rn = 1`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Quote
	for rows.Next() {
		quote, err := scanQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *quote)
	}
	return out, rows.Err()
}

// ---- items ----

func (s *Store) ListItems(ctx context.Context, ownerID string, cursor *uuid.UUID, limit int) ([]domain.Item, error) {
	query := `SELECT id, owner_id, title, payload, expires_at, created_at, updated_at
	          FROM catalog_user_items WHERE owner_id = ? AND expires_at > UTC_TIMESTAMP(3)`
	args := []any{ownerID}
	if cursor != nil {
		query += ` AND id > ?`
		args = append(args, db.UUIDBytes(*cursor))
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetItemByCreateKey(ctx context.Context, ownerID, key string) (*domain.Item, string, error) {
	var hash string
	item, err := scanItemWithHash(s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, title, payload, expires_at, created_at, updated_at, COALESCE(request_hash, '')
		FROM catalog_user_items WHERE owner_id = ? AND create_key = ?`, ownerID, key), &hash)
	return item, hash, err
}

func (s *Store) CreateItem(ctx context.Context, item domain.Item, createKey, requestHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_user_items (id, owner_id, title, payload, expires_at, create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(item.ID), item.OwnerID, item.Title, string(item.Payload), item.ExpiresAt,
		nullableString(createKey), nullableString(requestHash))
	return err
}

func (s *Store) DeleteItem(ctx context.Context, ownerID string, itemID uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM catalog_user_items WHERE id = ? AND owner_id = ?`, db.UUIDBytes(itemID), ownerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ---- helpers ----

const quoteSelect = `SELECT id, owner_id, sku_id, label_snapshot, source_kind, price_kind, price_fen,
	shipping_fen, currency, stock, source_url, specs_snapshot, discounts_snapshot,
	region_context, eligibility_context, observed_at, expires_at, schema_version
	FROM catalog_quote_snapshots `

func (s *Store) classifyUpdateMiss(ctx context.Context, table, ownerID string, entityID uuid.UUID) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(entityID), ownerID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrVersionConflict
}

func insertQuote(ctx context.Context, tx *sql.Tx, quote domain.Quote) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO catalog_quote_snapshots
		  (id, owner_id, sku_id, label_snapshot, source_kind, price_kind, price_fen,
		   shipping_fen, currency, stock, source_url, specs_snapshot, discounts_snapshot,
		   region_context, eligibility_context, observed_at, expires_at, schema_version,
		   create_key, request_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(quote.ID), quote.OwnerID, db.UUIDBytes(quote.SKUID), string(quote.LabelSnapshot),
		quote.SourceKind, quote.PriceKind, nullableInt64(quote.PriceFen), nullableInt64(quote.ShippingFen),
		quote.Currency, quote.Stock, nullableString(quote.SourceURL), string(quote.SpecsSnapshot), string(quote.Discounts),
		string(quote.RegionContext), string(quote.Eligibility), quote.ObservedAt, quote.ExpiresAt,
		quote.SchemaVersion, nil, nil)
	return err
}

func scanCategory(row interface{ Scan(...any) error }) (*domain.Category, error) {
	return scanCategoryWithHash(row, nil)
}

func scanCategoryWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Category, error) {
	var category domain.Category
	var rawID []byte
	dest := []any{&rawID, &category.OwnerID, &category.Name, &category.SortOrder, &category.Version, &category.CreatedAt, &category.UpdatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	category.ID = parsed
	return &category, nil
}

func scanProduct(row interface{ Scan(...any) error }) (*domain.Product, error) {
	return scanProductWithHash(row, nil)
}

func scanProductWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Product, error) {
	var product domain.Product
	var rawID []byte
	var categoryID []byte
	dest := []any{&rawID, &product.OwnerID, &categoryID, &product.Title, &product.CollectionState,
		&product.Version, &product.ExpiresAt, &product.CreatedAt, &product.UpdatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	product.ID = parsed
	if len(categoryID) > 0 {
		category, err := db.UUIDFromBytes(categoryID)
		if err != nil {
			return nil, err
		}
		product.CategoryID = &category
	}
	return &product, nil
}

func scanSKU(row interface{ Scan(...any) error }) (*domain.SKU, error) {
	return scanSKUWithHash(row, nil)
}

func scanSKUWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.SKU, error) {
	var sku domain.SKU
	var rawID, rawProduct []byte
	var attributes []byte
	dest := []any{&rawID, &sku.OwnerID, &rawProduct, &sku.DisplayName, &attributes,
		&sku.SchemaVersion, &sku.ResolutionState, &sku.Version, &sku.CreatedAt, &sku.UpdatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	product, err := db.UUIDFromBytes(rawProduct)
	if err != nil {
		return nil, err
	}
	sku.ID = parsed
	sku.ProductID = product
	sku.Attributes = attributes
	return &sku, nil
}

func scanQuote(row interface{ Scan(...any) error }) (*domain.Quote, error) {
	return scanQuoteWithHash(row, nil)
}

func scanQuoteWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Quote, error) {
	var quote domain.Quote
	var rawID, rawSKU []byte
	var label, specs, discounts, region, eligibility []byte
	var price, shipping sql.NullInt64
	var sourceURL sql.NullString
	dest := []any{&rawID, &quote.OwnerID, &rawSKU, &label, &quote.SourceKind, &quote.PriceKind,
		&price, &shipping, &quote.Currency, &quote.Stock, &sourceURL, &specs, &discounts, &region,
		&eligibility, &quote.ObservedAt, &quote.ExpiresAt, &quote.SchemaVersion}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	sku, err := db.UUIDFromBytes(rawSKU)
	if err != nil {
		return nil, err
	}
	quote.ID = parsed
	quote.SKUID = sku
	quote.LabelSnapshot = label
	quote.SpecsSnapshot = specs
	quote.Discounts = discounts
	quote.RegionContext = region
	quote.Eligibility = eligibility
	if price.Valid {
		value := price.Int64
		quote.PriceFen = &value
	}
	if shipping.Valid {
		value := shipping.Int64
		quote.ShippingFen = &value
	}
	if sourceURL.Valid {
		quote.SourceURL = sourceURL.String
	}
	return &quote, nil
}

func scanItem(row interface{ Scan(...any) error }) (*domain.Item, error) {
	return scanItemWithHash(row, nil)
}

func scanItemWithHash(row interface{ Scan(...any) error }, hash *string) (*domain.Item, error) {
	var item domain.Item
	var rawID []byte
	var payload []byte
	dest := []any{&rawID, &item.OwnerID, &item.Title, &payload, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, mapScanErr(err)
	}
	parsed, err := db.UUIDFromBytes(rawID)
	if err != nil {
		return nil, err
	}
	item.ID = parsed
	item.Payload = payload
	return &item, nil
}

func mapScanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func nullableUUID(value *uuid.UUID) any {
	if value == nil {
		return nil
	}
	return db.UUIDBytes(*value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
