package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/catalog/domain"
	catalogstore "github.com/mdhuser/spendwise-biz/internal/catalog/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type Service struct {
	store *catalogstore.Store
	now   func() time.Time
}

func New(store *catalogstore.Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock is used by tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

type CategoryInput struct {
	Name string `json:"name"`
}

type UpdateCategoryInput struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type ProductInput struct {
	Title      string  `json:"title"`
	CategoryID *string `json:"categoryId"`
}

type UpdateProductInput struct {
	Title      string  `json:"title"`
	CategoryID *string `json:"categoryId"`
	Version    int     `json:"version"`
}

type SKUInput struct {
	DisplayName string          `json:"displayName"`
	Attributes  json.RawMessage `json:"attributes"`
	Quote       *QuoteInput     `json:"quote"`
}

type UpdateSKUInput struct {
	DisplayName *string         `json:"displayName"`
	Attributes  json.RawMessage `json:"attributes"`
	Version     int             `json:"version"`
}

type QuoteInput struct {
	PriceFen    *int64 `json:"priceFen"`
	ShippingFen *int64 `json:"shippingFen"`
	SourceURL   string `json:"sourceUrl"`
}

type ItemInput struct {
	Title   string          `json:"title"`
	Payload json.RawMessage `json:"payload"`
}

type CategoryView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type QuoteView struct {
	ID          string    `json:"id"`
	SKUID       string    `json:"skuId"`
	PriceFen    *int64    `json:"priceFen"`
	ShippingFen *int64    `json:"shippingFen"`
	PriceKind   string    `json:"priceKind"`
	SourceKind  string    `json:"sourceKind"`
	SourceURL   string    `json:"sourceUrl,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type SKUView struct {
	ID          string          `json:"id"`
	ProductID   string          `json:"productId"`
	DisplayName string          `json:"displayName"`
	Attributes  json.RawMessage `json:"attributes"`
	Version     int             `json:"version"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	Quotes      []QuoteView     `json:"quotes"`
}

type ProductView struct {
	ID              string    `json:"id"`
	CategoryID      *string   `json:"categoryId"`
	Title           string    `json:"title"`
	CollectionState string    `json:"collectionState"`
	Version         int       `json:"version"`
	ExpiresAt       time.Time `json:"expiresAt"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	SKUs            []SKUView `json:"skus"`
}

type ProductList struct {
	Items      []ProductView `json:"items"`
	NextCursor *string       `json:"nextCursor"`
}

type ItemView struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Payload   json.RawMessage `json:"payload"`
	ExpiresAt time.Time       `json:"expiresAt"`
	CreatedAt time.Time       `json:"createdAt"`
}

type ItemList struct {
	Items      []ItemView `json:"items"`
	NextCursor *string    `json:"nextCursor"`
}

type LibraryView struct {
	Categories []CategoryView `json:"categories"`
	Products   []ProductView  `json:"products"`
	NextCursor *string        `json:"nextCursor"`
}

func (s *Service) ListCategories(ctx context.Context, ownerID string) ([]CategoryView, error) {
	categories, err := s.store.ListCategories(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]CategoryView, 0, len(categories))
	for _, category := range categories {
		out = append(out, categoryView(category))
	}
	return out, nil
}

func (s *Service) CreateCategory(ctx context.Context, ownerID, idempotencyKey string, input CategoryInput) (CategoryView, bool, error) {
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return CategoryView{}, false, err
	}
	name, err := validTitle(input.Name, "品类名称")
	if err != nil {
		return CategoryView{}, false, err
	}
	digest := requestDigest("catalog.category.create", input)
	count, err := s.store.CountCategories(ctx, ownerID)
	if err != nil {
		return CategoryView{}, false, err
	}
	if count >= domain.MaxCategories {
		return CategoryView{}, false, apperr.Validation("最多创建 100 个品类")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	category := domain.Category{ID: id.New(), OwnerID: ownerID, Name: name, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateCategory(ctx, category, key, digest); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetCategoryByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return CategoryView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				return categoryView(*existing), true, nil
			}
			if errors.Is(findErr, domain.ErrNotFound) {
				return CategoryView{}, false, apperr.Conflict("CONFLICT", "该品类已存在")
			}
			return CategoryView{}, false, findErr
		}
		return CategoryView{}, false, err
	}
	category, err = valueOrError(s.store.GetCategory(ctx, ownerID, category.ID))
	if err != nil {
		return CategoryView{}, false, err
	}
	return categoryView(category), false, nil
}

func (s *Service) UpdateCategory(ctx context.Context, ownerID, rawID string, input UpdateCategoryInput) (CategoryView, error) {
	categoryID, err := parseID(rawID)
	if err != nil {
		return CategoryView{}, err
	}
	name, err := validTitle(input.Name, "品类名称")
	if err != nil {
		return CategoryView{}, err
	}
	if input.Version < 1 {
		return CategoryView{}, apperr.Validation("version 必须为正整数")
	}
	category, err := s.store.UpdateCategory(ctx, ownerID, categoryID, name, normalizeName(name), input.Version)
	if err != nil {
		if db.IsDuplicate(err) {
			return CategoryView{}, apperr.Conflict("CONFLICT", "该品类已存在")
		}
		return CategoryView{}, mapError(err)
	}
	return categoryView(*category), nil
}

func (s *Service) DeleteCategory(ctx context.Context, ownerID, rawID string) error {
	categoryID, err := parseID(rawID)
	if err != nil {
		return err
	}
	return mapError(s.store.DeleteCategory(ctx, ownerID, categoryID))
}

func (s *Service) ListProducts(ctx context.Context, ownerID, cursor, categoryID string, limit int) (ProductList, error) {
	limit = clampLimit(limit)
	var cursorID *uuid.UUID
	if strings.TrimSpace(cursor) != "" {
		parsed, err := parseID(cursor)
		if err != nil {
			return ProductList{}, err
		}
		cursorID = &parsed
	}
	var category *uuid.UUID
	if strings.TrimSpace(categoryID) != "" {
		parsed, err := parseID(categoryID)
		if err != nil {
			return ProductList{}, err
		}
		category = &parsed
	}
	products, err := s.store.ListProducts(ctx, ownerID, cursorID, category, limit)
	if err != nil {
		return ProductList{}, err
	}
	next := ""
	if len(products) > limit {
		products = products[:limit]
		next = products[len(products)-1].ID.String()
	}
	views, err := s.assembleProducts(ctx, ownerID, products)
	if err != nil {
		return ProductList{}, err
	}
	result := ProductList{Items: views}
	if next != "" {
		result.NextCursor = &next
	}
	return result, nil
}

func (s *Service) GetProduct(ctx context.Context, ownerID, rawID string) (ProductView, error) {
	productID, err := parseID(rawID)
	if err != nil {
		return ProductView{}, err
	}
	product, err := s.store.GetProduct(ctx, ownerID, productID)
	if err != nil {
		return ProductView{}, mapError(err)
	}
	views, err := s.assembleProducts(ctx, ownerID, []domain.Product{*product})
	if err != nil {
		return ProductView{}, err
	}
	return views[0], nil
}

func (s *Service) CreateProduct(ctx context.Context, ownerID, idempotencyKey string, input ProductInput) (ProductView, bool, error) {
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return ProductView{}, false, err
	}
	title, err := validTitle(input.Title, "商品名称")
	if err != nil {
		return ProductView{}, false, err
	}
	categoryID, err := s.optionalCategory(ctx, ownerID, input.CategoryID)
	if err != nil {
		return ProductView{}, false, err
	}
	digest := requestDigest("catalog.product.create", input)
	count, err := s.store.CountProducts(ctx, ownerID)
	if err != nil {
		return ProductView{}, false, err
	}
	if count >= domain.MaxProducts {
		return ProductView{}, false, apperr.Validation("最多保留 500 个商品，请先清理不需要的商品")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	product := domain.Product{
		ID: id.New(), OwnerID: ownerID, CategoryID: categoryID, Title: title,
		CollectionState: "saved", Version: 1, ExpiresAt: now.Add(domain.ProductTTL), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateProduct(ctx, product, key, digest); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetProductByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return ProductView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				views, buildErr := s.assembleProducts(ctx, ownerID, []domain.Product{*existing})
				if buildErr != nil {
					return ProductView{}, false, buildErr
				}
				return views[0], true, nil
			}
			return ProductView{}, false, apperr.Conflict("CONFLICT", "商品创建冲突，请重试")
		}
		return ProductView{}, false, err
	}
	views, err := s.assembleProducts(ctx, ownerID, []domain.Product{product})
	if err != nil {
		return ProductView{}, false, err
	}
	return views[0], false, nil
}

func (s *Service) UpdateProduct(ctx context.Context, ownerID, rawID string, input UpdateProductInput) (ProductView, error) {
	productID, err := parseID(rawID)
	if err != nil {
		return ProductView{}, err
	}
	title, err := validTitle(input.Title, "商品名称")
	if err != nil {
		return ProductView{}, err
	}
	if input.Version < 1 {
		return ProductView{}, apperr.Validation("version 必须为正整数")
	}
	categoryID, err := s.optionalCategory(ctx, ownerID, input.CategoryID)
	if err != nil {
		return ProductView{}, err
	}
	product, err := s.store.UpdateProduct(ctx, ownerID, productID, title, categoryID, input.Version)
	if err != nil {
		return ProductView{}, mapError(err)
	}
	views, err := s.assembleProducts(ctx, ownerID, []domain.Product{*product})
	if err != nil {
		return ProductView{}, err
	}
	return views[0], nil
}

func (s *Service) DeleteProduct(ctx context.Context, ownerID, rawID string) error {
	productID, err := parseID(rawID)
	if err != nil {
		return err
	}
	return mapError(s.store.DeleteProduct(ctx, ownerID, productID))
}

func (s *Service) ListSKUs(ctx context.Context, ownerID, rawProductID string) ([]SKUView, error) {
	productID, err := parseID(rawProductID)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetProduct(ctx, ownerID, productID); err != nil {
		return nil, mapError(err)
	}
	skus, err := s.store.ListSKUs(ctx, ownerID, productID)
	if err != nil {
		return nil, err
	}
	quotes, err := s.store.LatestQuotes(ctx, ownerID, skuIDs(skus))
	if err != nil {
		return nil, err
	}
	quoteMap := groupQuotes(quotes)
	out := make([]SKUView, 0, len(skus))
	for _, sku := range skus {
		out = append(out, skuView(sku, quoteMap[sku.ID]))
	}
	return out, nil
}

func (s *Service) CreateSKU(ctx context.Context, ownerID, rawProductID, idempotencyKey string, input SKUInput) (SKUView, bool, error) {
	productID, err := parseID(rawProductID)
	if err != nil {
		return SKUView{}, false, err
	}
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return SKUView{}, false, err
	}
	displayName, err := validTitle(input.DisplayName, "规格名称")
	if err != nil {
		return SKUView{}, false, err
	}
	attributes, err := normalizeAttributes(input.Attributes)
	if err != nil {
		return SKUView{}, false, err
	}
	product, err := s.store.GetProduct(ctx, ownerID, productID)
	if err != nil {
		return SKUView{}, false, mapError(err)
	}
	count, err := s.store.CountSKUs(ctx, ownerID, productID)
	if err != nil {
		return SKUView{}, false, err
	}
	if count >= domain.MaxSKUsPerProduct {
		return SKUView{}, false, apperr.Validation("每个商品最多 20 个规格")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	sku := domain.SKU{
		ID: id.New(), OwnerID: ownerID, ProductID: productID, DisplayName: displayName,
		Attributes: attributes, SchemaVersion: 1, ResolutionState: "confirmed", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	var quote *domain.Quote
	if input.Quote != nil {
		built, err := s.buildQuote(*product, sku, *input.Quote, now)
		if err != nil {
			return SKUView{}, false, err
		}
		quote = &built
	}
	digest := requestDigest("catalog.sku.create:"+productID.String(), input)
	createdQuote, err := s.store.CreateSKU(ctx, sku, key, digest, quote)
	if err != nil {
		if db.IsDuplicate(err) {
			existing, existingQuote, existingHash, findErr := s.store.GetSKUByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return SKUView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				var quotes []domain.Quote
				if existingQuote != nil {
					quotes = []domain.Quote{*existingQuote}
				}
				return skuView(*existing, quotes), true, nil
			}
			return SKUView{}, false, apperr.Conflict("CONFLICT", "规格创建冲突，请重试")
		}
		return SKUView{}, false, err
	}
	var quotes []domain.Quote
	if createdQuote != nil {
		quotes = []domain.Quote{*createdQuote}
	}
	return skuView(sku, quotes), false, nil
}

func (s *Service) UpdateSKU(ctx context.Context, ownerID, rawID string, input UpdateSKUInput) (SKUView, error) {
	skuID, err := parseID(rawID)
	if err != nil {
		return SKUView{}, err
	}
	existing, err := s.store.GetSKU(ctx, ownerID, skuID)
	if err != nil {
		return SKUView{}, mapError(err)
	}
	displayName := existing.DisplayName
	if input.DisplayName != nil {
		displayName, err = validTitle(*input.DisplayName, "规格名称")
		if err != nil {
			return SKUView{}, err
		}
	}
	attributes := existing.Attributes
	if len(bytes.TrimSpace(input.Attributes)) > 0 {
		attributes, err = normalizeAttributes(input.Attributes)
		if err != nil {
			return SKUView{}, err
		}
	}
	if input.Version < 1 {
		return SKUView{}, apperr.Validation("version 必须为正整数")
	}
	updated, err := s.store.UpdateSKU(ctx, ownerID, skuID, displayName, attributes, input.Version)
	if err != nil {
		return SKUView{}, mapError(err)
	}
	quotes, err := s.store.LatestQuotes(ctx, ownerID, []uuid.UUID{skuID})
	if err != nil {
		return SKUView{}, err
	}
	return skuView(*updated, quotes), nil
}

func (s *Service) DeleteSKU(ctx context.Context, ownerID, rawID string) error {
	skuID, err := parseID(rawID)
	if err != nil {
		return err
	}
	return mapError(s.store.DeleteSKU(ctx, ownerID, skuID))
}

func (s *Service) CreateQuote(ctx context.Context, ownerID, rawSKUID, idempotencyKey string, input QuoteInput) (QuoteView, bool, error) {
	skuID, err := parseID(rawSKUID)
	if err != nil {
		return QuoteView{}, false, err
	}
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return QuoteView{}, false, err
	}
	sku, err := s.store.GetSKU(ctx, ownerID, skuID)
	if err != nil {
		return QuoteView{}, false, mapError(err)
	}
	product, err := s.store.GetProduct(ctx, ownerID, sku.ProductID)
	if err != nil {
		return QuoteView{}, false, mapError(err)
	}
	quote, err := s.buildQuote(*product, *sku, input, s.now().UTC().Truncate(time.Millisecond))
	if err != nil {
		return QuoteView{}, false, err
	}
	digest := requestDigest("catalog.quote.create:"+skuID.String(), input)
	if err := s.store.CreateQuote(ctx, quote, key, digest); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetQuoteByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return QuoteView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				return quoteView(*existing), true, nil
			}
			return QuoteView{}, false, apperr.Conflict("CONFLICT", "报价创建冲突，请重试")
		}
		return QuoteView{}, false, err
	}
	return quoteView(quote), false, nil
}

func (s *Service) Library(ctx context.Context, ownerID, cursor string, limit int) (LibraryView, error) {
	limit = clampLimit(limit)
	var cursorID *uuid.UUID
	if strings.TrimSpace(cursor) != "" {
		parsed, err := parseID(cursor)
		if err != nil {
			return LibraryView{}, err
		}
		cursorID = &parsed
	}
	categories, err := s.store.ListCategories(ctx, ownerID)
	if err != nil {
		return LibraryView{}, err
	}
	products, err := s.store.ListProducts(ctx, ownerID, cursorID, nil, limit)
	if err != nil {
		return LibraryView{}, err
	}
	next := ""
	if len(products) > limit {
		products = products[:limit]
		next = products[len(products)-1].ID.String()
	}
	views, err := s.assembleProducts(ctx, ownerID, products)
	if err != nil {
		return LibraryView{}, err
	}
	categoryViews := make([]CategoryView, 0, len(categories))
	for _, category := range categories {
		categoryViews = append(categoryViews, categoryView(category))
	}
	result := LibraryView{Categories: categoryViews, Products: views}
	if next != "" {
		result.NextCursor = &next
	}
	return result, nil
}

func (s *Service) ListItems(ctx context.Context, ownerID, cursor string, limit int) (ItemList, error) {
	limit = clampLimit(limit)
	var cursorID *uuid.UUID
	if strings.TrimSpace(cursor) != "" {
		parsed, err := parseID(cursor)
		if err != nil {
			return ItemList{}, err
		}
		cursorID = &parsed
	}
	items, err := s.store.ListItems(ctx, ownerID, cursorID, limit)
	if err != nil {
		return ItemList{}, err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID.String()
	}
	out := make([]ItemView, 0, len(items))
	for _, item := range items {
		out = append(out, ItemView{ID: item.ID.String(), Title: item.Title, Payload: item.Payload, ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt})
	}
	result := ItemList{Items: out}
	if next != "" {
		result.NextCursor = &next
	}
	return result, nil
}

func (s *Service) CreateItem(ctx context.Context, ownerID, idempotencyKey string, input ItemInput) (ItemView, bool, error) {
	key, err := requireKey(idempotencyKey)
	if err != nil {
		return ItemView{}, false, err
	}
	title, err := validTitle(input.Title, "清单条目名称")
	if err != nil {
		return ItemView{}, false, err
	}
	payload := input.Payload
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) || len(payload) > 32<<10 {
		return ItemView{}, false, apperr.Validation("payload 必须是合法的 JSON 且不超过 32KB")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	item := domain.Item{ID: id.New(), OwnerID: ownerID, Title: title, Payload: payload, ExpiresAt: now.Add(domain.ProductTTL), CreatedAt: now, UpdatedAt: now}
	digest := requestDigest("catalog.item.create", input)
	if err := s.store.CreateItem(ctx, item, key, digest); err != nil {
		if db.IsDuplicate(err) {
			existing, existingHash, findErr := s.store.GetItemByCreateKey(ctx, ownerID, key)
			if findErr == nil {
				if existingHash != digest {
					return ItemView{}, false, apperr.Conflict("IDEMPOTENCY_MISMATCH", "Idempotency-Key 已用于不同请求")
				}
				return ItemView{ID: existing.ID.String(), Title: existing.Title, Payload: existing.Payload, ExpiresAt: existing.ExpiresAt, CreatedAt: existing.CreatedAt}, true, nil
			}
			return ItemView{}, false, apperr.Conflict("CONFLICT", "清单条目创建冲突，请重试")
		}
		return ItemView{}, false, err
	}
	return ItemView{ID: item.ID.String(), Title: item.Title, Payload: item.Payload, ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt}, false, nil
}

func (s *Service) DeleteItem(ctx context.Context, ownerID, rawID string) error {
	itemID, err := parseID(rawID)
	if err != nil {
		return err
	}
	return mapError(s.store.DeleteItem(ctx, ownerID, itemID))
}

// ---- helpers ----

func (s *Service) assembleProducts(ctx context.Context, ownerID string, products []domain.Product) ([]ProductView, error) {
	ids := make([]uuid.UUID, 0, len(products))
	for _, product := range products {
		ids = append(ids, product.ID)
	}
	skus, err := s.store.ListSKUsForProducts(ctx, ownerID, ids)
	if err != nil {
		return nil, err
	}
	quotes, err := s.store.LatestQuotes(ctx, ownerID, skuIDs(skus))
	if err != nil {
		return nil, err
	}
	quoteMap := groupQuotes(quotes)
	skuMap := map[uuid.UUID][]SKUView{}
	for _, sku := range skus {
		skuMap[sku.ProductID] = append(skuMap[sku.ProductID], skuView(sku, quoteMap[sku.ID]))
	}
	out := make([]ProductView, 0, len(products))
	for _, product := range products {
		view := ProductView{
			ID: product.ID.String(), Title: product.Title, CollectionState: product.CollectionState,
			Version: product.Version, ExpiresAt: product.ExpiresAt, CreatedAt: product.CreatedAt, UpdatedAt: product.UpdatedAt,
			SKUs: skuMap[product.ID],
		}
		if view.SKUs == nil {
			view.SKUs = []SKUView{}
		}
		if product.CategoryID != nil {
			value := product.CategoryID.String()
			view.CategoryID = &value
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Service) optionalCategory(ctx context.Context, ownerID string, raw *string) (*uuid.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	categoryID, err := parseID(*raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetCategory(ctx, ownerID, categoryID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, apperr.Validation("品类不存在")
		}
		return nil, err
	}
	return &categoryID, nil
}

func (s *Service) buildQuote(product domain.Product, sku domain.SKU, input QuoteInput, now time.Time) (domain.Quote, error) {
	if input.PriceFen != nil && (*input.PriceFen < 0 || *input.PriceFen > domain.MaxPriceFen) {
		return domain.Quote{}, apperr.Validation("priceFen 必须在 0 到 1000000000 之间")
	}
	if input.ShippingFen != nil && (*input.ShippingFen < 0 || *input.ShippingFen > domain.MaxPriceFen) {
		return domain.Quote{}, apperr.Validation("shippingFen 必须在 0 到 1000000000 之间")
	}
	sourceURL := strings.TrimSpace(input.SourceURL)
	if sourceURL != "" {
		parsed, err := url.Parse(sourceURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || len(sourceURL) > 2000 {
			return domain.Quote{}, apperr.Validation("sourceUrl 必须是合法的 http(s) 链接")
		}
	}
	label, err := json.Marshal(map[string]string{"title": product.Title, "skuName": sku.DisplayName})
	if err != nil {
		return domain.Quote{}, err
	}
	priceKind := "unknown"
	if input.PriceFen != nil {
		priceKind = "exact"
	}
	expires := now.Add(domain.QuoteTTL)
	if product.ExpiresAt.Before(expires) {
		expires = product.ExpiresAt
	}
	return domain.Quote{
		ID: id.New(), OwnerID: sku.OwnerID, SKUID: sku.ID,
		LabelSnapshot: label, SourceKind: "user_input", PriceKind: priceKind,
		PriceFen: input.PriceFen, ShippingFen: input.ShippingFen,
		Currency: "CNY", Stock: "unknown", SourceURL: sourceURL,
		SpecsSnapshot: sku.Attributes, Discounts: json.RawMessage(`[]`),
		RegionContext: json.RawMessage(`{}`), Eligibility: json.RawMessage(`{}`),
		ObservedAt: now, ExpiresAt: expires, SchemaVersion: 3,
	}, nil
}

func validTitle(value, field string) (string, error) {
	trimmed := strings.TrimSpace(value)
	length := utf8.RuneCountInString(trimmed)
	if length < 1 || length > 120 {
		return "", apperr.Validation(field + "长度必须在 1 到 120 个字符之间")
	}
	return trimmed, nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeAttributes(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	var attributes map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &attributes); err != nil {
		return nil, apperr.Validation("attributes 必须是 JSON 对象")
	}
	if len(attributes) > domain.MaxAttributes {
		return nil, apperr.Validation("最多 40 个规格属性")
	}
	for key, rawAttribute := range attributes {
		if err := validateAttributeKey(key); err != nil {
			return nil, err
		}
		var attribute struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
			Unit  string          `json:"unit"`
		}
		decoder := json.NewDecoder(bytes.NewReader(rawAttribute))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&attribute); err != nil {
			return nil, apperr.Validation("规格属性 " + key + " 结构不合法")
		}
		unit := strings.TrimSpace(attribute.Unit)
		value := bytes.TrimSpace(attribute.Value)
		switch attribute.Type {
		case "number":
			if unit == "" || utf8.RuneCountInString(unit) > 20 {
				return nil, apperr.Validation("数值属性 " + key + " 必须提供 1-20 字符的单位")
			}
			if !bytes.Equal(value, []byte("null")) {
				decoder := json.NewDecoder(bytes.NewReader(value))
				decoder.UseNumber()
				var number any
				if err := decoder.Decode(&number); err != nil {
					return nil, apperr.Validation("数值属性 " + key + " 的 value 必须是数字")
				}
				parsed, ok := number.(json.Number)
				if !ok {
					return nil, apperr.Validation("数值属性 " + key + " 的 value 必须是数字")
				}
				floatValue, err := parsed.Float64()
				if err != nil || math.IsNaN(floatValue) || math.IsInf(floatValue, 0) || floatValue < 0 || floatValue > 1e9 {
					return nil, apperr.Validation("数值属性 " + key + " 超出允许范围")
				}
			}
		case "text":
			if !bytes.Equal(value, []byte("null")) {
				var text string
				if err := json.Unmarshal(value, &text); err != nil || utf8.RuneCountInString(text) > 200 {
					return nil, apperr.Validation("文本属性 " + key + " 的 value 必须是不超过 200 字符的字符串")
				}
			}
		case "boolean":
			if !bytes.Equal(value, []byte("null")) {
				var boolean bool
				if err := json.Unmarshal(value, &boolean); err != nil {
					return nil, apperr.Validation("布尔属性 " + key + " 的 value 必须是布尔值")
				}
			}
		default:
			return nil, apperr.Validation("规格属性 " + key + " 的 type 必须是 number/text/boolean")
		}
	}
	normalized, err := json.Marshal(attributes)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateAttributeKey(key string) error {
	length := utf8.RuneCountInString(key)
	if length < 1 || length > 40 {
		return apperr.Validation("规格属性名长度必须在 1 到 40 个字符之间")
	}
	for index, char := range key {
		if index == 0 {
			if !unicode.IsLetter(char) {
				return apperr.Validation("规格属性名必须以字母开头")
			}
			continue
		}
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return apperr.Validation("规格属性名只能包含字母、数字和下划线")
		}
	}
	return nil
}

func requireKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 128 {
		return "", apperr.New(400, "IDEMPOTENCY_KEY_REQUIRED", "必须提供 8-128 字符的 Idempotency-Key")
	}
	return value, nil
}

func requestDigest(scope string, payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(scope)
	}
	hash := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	return hex.EncodeToString(hash[:])
}

func parseID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, apperr.Validation("id 不合法")
	}
	return parsed, nil
}

func clampLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 100 {
		return 100
	}
	return value
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound("商品或品类不存在")
	case errors.Is(err, domain.ErrVersionConflict):
		return apperr.Conflict("VERSION_CONFLICT", "数据已被其他请求修改，请刷新后重试")
	default:
		return err
	}
}

func valueOrError[T any](value *T, err error) (T, error) {
	if err != nil {
		return *new(T), err
	}
	return *value, nil
}

func categoryView(category domain.Category) CategoryView {
	return CategoryView{ID: category.ID.String(), Name: category.Name, Version: category.Version, CreatedAt: category.CreatedAt, UpdatedAt: category.UpdatedAt}
}

func skuView(sku domain.SKU, quotes []domain.Quote) SKUView {
	view := SKUView{
		ID: sku.ID.String(), ProductID: sku.ProductID.String(), DisplayName: sku.DisplayName,
		Attributes: sku.Attributes, Version: sku.Version, CreatedAt: sku.CreatedAt, UpdatedAt: sku.UpdatedAt,
		Quotes: []QuoteView{},
	}
	if len(view.Attributes) == 0 {
		view.Attributes = json.RawMessage(`{}`)
	}
	for _, quote := range quotes {
		view.Quotes = append(view.Quotes, quoteView(quote))
	}
	return view
}

func quoteView(quote domain.Quote) QuoteView {
	return QuoteView{
		ID: quote.ID.String(), SKUID: quote.SKUID.String(), PriceFen: quote.PriceFen,
		ShippingFen: quote.ShippingFen, PriceKind: quote.PriceKind, SourceKind: quote.SourceKind,
		SourceURL: quote.SourceURL, ObservedAt: quote.ObservedAt, ExpiresAt: quote.ExpiresAt,
	}
}

func skuIDs(skus []domain.SKU) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(skus))
	for _, sku := range skus {
		out = append(out, sku.ID)
	}
	return out
}

func groupQuotes(quotes []domain.Quote) map[uuid.UUID][]domain.Quote {
	out := map[uuid.UUID][]domain.Quote{}
	for _, quote := range quotes {
		out[quote.SKUID] = append(out[quote.SKUID], quote)
	}
	return out
}
