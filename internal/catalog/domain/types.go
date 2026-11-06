// Package domain contains catalog entities and validation rules. It has no
// database or HTTP dependencies.
package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound        = errors.New("catalog: not found")
	ErrVersionConflict = errors.New("catalog: version conflict")
	ErrDuplicate       = errors.New("catalog: duplicate")
	ErrLimit           = errors.New("catalog: limit reached")
)

const (
	MaxCategories     = 100
	MaxProducts       = 500
	MaxSKUsPerProduct = 20
	MaxAttributes     = 40
	MaxPriceFen       = 1_000_000_000
	QuoteTTL          = 24 * time.Hour
	ProductTTL        = 30 * 24 * time.Hour
)

// Category is a user-owned catalog category.
type Category struct {
	ID        uuid.UUID
	OwnerID   string
	Name      string
	SortOrder int
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Product is a user-owned SPU / saved library item.
type Product struct {
	ID              uuid.UUID
	OwnerID         string
	CategoryID      *uuid.UUID
	Title           string
	CollectionState string
	Version         int
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SKU is a user-owned variant of a product.
type SKU struct {
	ID              uuid.UUID
	OwnerID         string
	ProductID       uuid.UUID
	DisplayName     string
	Attributes      json.RawMessage
	SchemaVersion   int
	ResolutionState string
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Quote is one price observation. PriceFen and ShippingFen are nil when the
// value is unknown; they are never silently coerced to zero.
type Quote struct {
	ID            uuid.UUID
	OwnerID       string
	SKUID         uuid.UUID
	LabelSnapshot json.RawMessage
	SourceKind    string
	PriceKind     string
	PriceFen      *int64
	ShippingFen   *int64
	Currency      string
	Stock         string
	SourceURL     string
	SpecsSnapshot json.RawMessage
	Discounts     json.RawMessage
	RegionContext json.RawMessage
	Eligibility   json.RawMessage
	ObservedAt    time.Time
	ExpiresAt     time.Time
	SchemaVersion int
}

// Item is a free-form personal library entry.
type Item struct {
	ID        uuid.UUID
	OwnerID   string
	Title     string
	Payload   json.RawMessage
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}
