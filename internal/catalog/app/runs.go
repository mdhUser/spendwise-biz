package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/catalog/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
)

// ComparisonQuote is the minimal quote view the worker needs. Nil price or
// shipping means unknown.
type ComparisonQuote struct {
	ID          string `json:"id"`
	PriceFen    *int64 `json:"priceFen"`
	ShippingFen *int64 `json:"shippingFen"`
	PriceKind   string `json:"priceKind"`
}

type ComparisonItem struct {
	SKUID        string           `json:"skuId"`
	DisplayName  string           `json:"displayName"`
	ProductTitle string           `json:"productTitle"`
	Quote        *ComparisonQuote `json:"quote"`
}

type ComparisonData struct {
	Source     *ComparisonItem  `json:"source"`
	Candidates []ComparisonItem `json:"candidates"`
}

// ComparisonData loads source/candidate SKUs and their latest quotes for the
// worker. It only reads catalog-owned tables and is called by the worker's
// composition layer, never by conversation HTTP handlers.
func (s *Service) ComparisonData(ctx context.Context, ownerID, sourceSKUID string, candidateSKUIDs []string) (ComparisonData, error) {
	var result ComparisonData
	sourceID, err := uuid.Parse(strings.TrimSpace(sourceSKUID))
	if err != nil {
		return result, apperr.Validation("sourceSkuId 不合法")
	}
	source, err := s.comparisonItem(ctx, ownerID, sourceID)
	if err != nil {
		return result, mapError(err)
	}
	result.Source = &source

	if len(candidateSKUIDs) > 0 {
		for _, raw := range candidateSKUIDs {
			candidateID, err := uuid.Parse(strings.TrimSpace(raw))
			if err != nil || candidateID == sourceID {
				continue
			}
			item, err := s.comparisonItem(ctx, ownerID, candidateID)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				return result, err
			}
			result.Candidates = append(result.Candidates, item)
		}
		return result, nil
	}

	rows, err := s.store.ListRecentSKUsWithProduct(ctx, ownerID, sourceID, 5)
	if err != nil {
		return result, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	quotes, err := s.store.LatestQuotes(ctx, ownerID, ids)
	if err != nil {
		return result, err
	}
	quoteMap := groupQuotes(quotes)
	for _, row := range rows {
		item := ComparisonItem{
			SKUID: row.ID.String(), DisplayName: row.DisplayName,
			ProductTitle: row.ProductTitle, Quote: comparisonQuote(quoteMap[row.ID]),
		}
		result.Candidates = append(result.Candidates, item)
	}
	return result, nil
}

func (s *Service) comparisonItem(ctx context.Context, ownerID string, skuID uuid.UUID) (ComparisonItem, error) {
	sku, err := s.store.GetSKU(ctx, ownerID, skuID)
	if err != nil {
		return ComparisonItem{}, err
	}
	product, err := s.store.GetProduct(ctx, ownerID, sku.ProductID)
	if err != nil {
		return ComparisonItem{}, err
	}
	quotes, err := s.store.LatestQuotes(ctx, ownerID, []uuid.UUID{skuID})
	if err != nil {
		return ComparisonItem{}, err
	}
	return ComparisonItem{
		SKUID: sku.ID.String(), DisplayName: sku.DisplayName,
		ProductTitle: product.Title, Quote: comparisonQuote(quotes),
	}, nil
}

func comparisonQuote(quotes []domain.Quote) *ComparisonQuote {
	if len(quotes) == 0 {
		return nil
	}
	quote := quotes[0]
	return &ComparisonQuote{ID: quote.ID.String(), PriceFen: quote.PriceFen, ShippingFen: quote.ShippingFen, PriceKind: quote.PriceKind}
}
