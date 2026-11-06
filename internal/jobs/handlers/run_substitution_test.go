package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	catalogapp "github.com/mdhuser/spendwise-biz/internal/catalog/app"
)

func price(value int64) *int64 { return &value }

func TestCompareKeepsUnknownsAndComputesSaving(t *testing.T) {
	data := catalogapp.ComparisonData{
		Source: &catalogapp.ComparisonItem{
			SKUID: "source", DisplayName: "500只", ProductTitle: "垃圾袋",
			Quote: &catalogapp.ComparisonQuote{ID: "q-source", PriceFen: price(1000), ShippingFen: price(200)},
		},
		Candidates: []catalogapp.ComparisonItem{
			{
				SKUID: "candidate-1", DisplayName: "1000只", ProductTitle: "加厚垃圾袋",
				Quote: &catalogapp.ComparisonQuote{ID: "q-1", PriceFen: price(1500), ShippingFen: price(100)},
			},
			{
				SKUID: "candidate-2", DisplayName: "未知报价", ProductTitle: "其他垃圾袋",
				Quote: &catalogapp.ComparisonQuote{ID: "q-2", PriceFen: nil, ShippingFen: price(0)},
			},
			{
				SKUID: "candidate-3", DisplayName: "未知运费", ProductTitle: "其他垃圾袋",
				Quote: &catalogapp.ComparisonQuote{ID: "q-3", PriceFen: price(800), ShippingFen: nil},
			},
		},
	}
	assessments, summary := compare(data)
	if len(assessments) != 3 {
		t.Fatalf("assessments = %d, want 3", len(assessments))
	}
	if assessments[0].Verdict != "alternative" {
		t.Fatalf("known candidate verdict = %q", assessments[0].Verdict)
	}
	var breakdown map[string]any
	if err := json.Unmarshal(assessments[0].CostBreakdown, &breakdown); err != nil {
		t.Fatal(err)
	}
	if breakdown["sourceTotalFen"].(float64) != 1200 || breakdown["candidateTotalFen"].(float64) != 1600 {
		t.Fatalf("breakdown = %v", breakdown)
	}
	if assessments[1].Verdict != "unknown" || assessments[2].Verdict != "unknown" {
		t.Fatalf("unknown candidates must stay unknown: %q %q", assessments[1].Verdict, assessments[2].Verdict)
	}
	if !strings.Contains(summary, "1 个有完整报价") || !strings.Contains(summary, "未知价格没有按 0 处理") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCompareWithoutCandidates(t *testing.T) {
	data := catalogapp.ComparisonData{Source: &catalogapp.ComparisonItem{ProductTitle: "x", DisplayName: "y", Quote: &catalogapp.ComparisonQuote{PriceFen: price(100), ShippingFen: price(0)}}}
	assessments, summary := compare(data)
	if len(assessments) != 0 || !strings.Contains(summary, "没有可比较的候选规格") {
		t.Fatalf("assessments=%d summary=%q", len(assessments), summary)
	}
}
