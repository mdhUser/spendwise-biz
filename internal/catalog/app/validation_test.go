package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mdhuser/spendwise-biz/internal/catalog/domain"
)

func TestNormalizeAttributesAcceptsSupportedShapes(t *testing.T) {
	raw := json.RawMessage(`{
		"countPerPack":{"type":"number","value":100,"unit":"piece"},
		"leakProof":{"type":"boolean","value":true},
		"material":{"type":"text","value":"PE"},
		"unknownSize":{"type":"number","value":null,"unit":"cm"}
	}`)
	normalized, err := normalizeAttributes(raw)
	if err != nil {
		t.Fatalf("normalizeAttributes: %v", err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		t.Fatalf("normalized JSON: %v", err)
	}
	if len(decoded) != 4 {
		t.Fatalf("attributes = %d, want 4", len(decoded))
	}
}

func TestNormalizeAttributesRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"too many":       `{` + repeatedKeys(41) + `}`,
		"bad key":        `{"1bad":{"type":"text","value":"x"}}`,
		"bad type":       `{"size":{"type":"date","value":"today"}}`,
		"missing unit":   `{"size":{"type":"number","value":1}}`,
		"negative":       `{"size":{"type":"number","value":-1,"unit":"cm"}}`,
		"too long text":  `{"note":{"type":"text","value":"` + strings.Repeat("x", 201) + `"}}`,
		"unknown field":  `{"size":{"type":"number","value":1,"unit":"cm","extra":true}}`,
		"boolean string": `{"ok":{"type":"boolean","value":"true"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeAttributes(json.RawMessage(raw)); err == nil {
				t.Fatalf("expected validation error for %s", raw)
			}
		})
	}
}

func TestBuildQuoteKeepsUnknownsUnknown(t *testing.T) {
	service := &Service{now: func() time.Time { return time.Unix(1000, 0).UTC() }}
	expires := time.Unix(200000, 0).UTC()
	product := domain.Product{Title: "垃圾袋", ExpiresAt: expires}
	sku := domain.SKU{DisplayName: "100只", Attributes: json.RawMessage(`{"count":{"type":"number","value":100,"unit":"piece"}}`)}
	quote, err := service.buildQuote(product, sku, QuoteInput{PriceFen: nil, ShippingFen: nil}, time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	if quote.PriceKind != "unknown" || quote.PriceFen != nil || quote.ShippingFen != nil {
		t.Fatalf("unknown quote was coerced: %+v", quote)
	}
	zero := int64(0)
	quote, err = service.buildQuote(product, sku, QuoteInput{PriceFen: &zero, ShippingFen: nil}, time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatalf("buildQuote priced: %v", err)
	}
	if quote.PriceKind != "exact" || quote.PriceFen == nil || *quote.PriceFen != 0 {
		t.Fatalf("zero price must remain exact/zero: %+v", quote)
	}
	if quote.ShippingFen != nil {
		t.Fatal("unknown shipping must remain nil, not zero")
	}
}

func TestBuildQuoteValidatesSourceURL(t *testing.T) {
	service := &Service{now: time.Now}
	product := domain.Product{Title: "x", ExpiresAt: time.Now().Add(time.Hour)}
	sku := domain.SKU{DisplayName: "y", Attributes: json.RawMessage(`{}`)}
	for _, value := range []string{"javascript:alert(1)", "ftp://example.com/x", "https://user:pass@example.com/x", "not a url"} {
		if _, err := service.buildQuote(product, sku, QuoteInput{SourceURL: value}, time.Now()); err == nil {
			t.Fatalf("sourceUrl %q should be rejected", value)
		}
	}
	if _, err := service.buildQuote(product, sku, QuoteInput{SourceURL: "https://example.com/item"}, time.Now()); err != nil {
		t.Fatalf("valid sourceUrl rejected: %v", err)
	}
}

func TestValidTitle(t *testing.T) {
	if _, err := validTitle("  ", "名称"); err == nil {
		t.Fatal("blank title should fail")
	}
	if _, err := validTitle(strings.Repeat("好", 121), "名称"); err == nil {
		t.Fatal("overlong title should fail")
	}
	if value, err := validTitle("  露营用品  ", "名称"); err != nil || value != "露营用品" {
		t.Fatalf("trim = %q, err = %v", value, err)
	}
}

func repeatedKeys(count int) string {
	parts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		key := "a" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		parts = append(parts, `"`+key+`":{"type":"text","value":null}`)
	}
	return strings.Join(parts, ",")
}
