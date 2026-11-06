// Package handlers contains worker job handlers. It composes module
// app.Services; it never reads another module's tables directly.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	catalogapp "github.com/mdhuser/spendwise-biz/internal/catalog/app"
	conversationapp "github.com/mdhuser/spendwise-biz/internal/conversation/app"
	"github.com/mdhuser/spendwise-biz/internal/conversation/domain"
	"github.com/mdhuser/spendwise-biz/internal/jobs"
	"github.com/mdhuser/spendwise-biz/internal/llm"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
)

type RunSubstitution struct {
	Conversations *conversationapp.Service
	Catalog       *catalogapp.Service
	LLM           *llm.Router
	Logger        *slog.Logger
}

func (h *RunSubstitution) Handle(ctx context.Context, job jobs.Job) error {
	if job.Kind != "run.substitution" {
		return fmt.Errorf("run.substitution: unsupported kind %q", job.Kind)
	}
	if job.RunID == nil {
		return errors.New("run.substitution: job has no run id")
	}
	run, err := h.Conversations.RunJob(ctx, job.OwnerID, *job.RunID)
	if err != nil {
		return err
	}
	if err := h.Conversations.MarkRunRunning(ctx, job.OwnerID, run.RunID); err != nil {
		if errors.Is(err, domain.ErrRunCancelled) {
			return nil
		}
		return err
	}

	data, err := h.Catalog.ComparisonData(ctx, job.OwnerID, run.SourceSKUID, run.CandidateSKUIDs)
	if err != nil {
		if isNotFound(err) || errors.Is(err, domain.ErrRunCancelled) {
			return h.failRun(ctx, job.OwnerID, run.RunID, "源商品或候选商品不存在，请重新选择。")
		}
		return err
	}
	if data.Source == nil || data.Source.Quote == nil || data.Source.Quote.PriceFen == nil {
		return h.failRun(ctx, job.OwnerID, run.RunID, "源规格缺少可用报价，无法比较；未知价格不会被当作 0。")
	}

	assessments, summary := compare(data)
	if h.LLM != nil {
		reply, llmErr := h.LLM.Complete(ctx, llm.TaskDecide, []llm.Message{
			{Role: "system", Content: "你是购物平替助手。只能基于给定报价与规格回答，不得编造价格、链接或库存。"},
			{Role: "user", Content: summary},
		})
		if llmErr == nil && strings.TrimSpace(reply) != "" {
			summary = summary + "\n" + strings.TrimSpace(reply)
		} else if llmErr != nil && h.Logger != nil {
			h.Logger.Warn("llm_fallback", "run_id", run.RunID.String(), "error", llmErr)
		}
	}

	if err := h.Conversations.CompleteRun(ctx, job.OwnerID, run.RunID, summary, assessments, summary); err != nil {
		if errors.Is(err, domain.ErrRunCancelled) {
			return nil
		}
		return err
	}
	return nil
}

func (h *RunSubstitution) failRun(ctx context.Context, ownerID string, runID uuid.UUID, reason string) error {
	return h.Conversations.FailRun(ctx, ownerID, runID, reason)
}

func isNotFound(err error) bool {
	var known *apperr.Error
	return errors.As(err, &known) && known.Status == 404
}

func compare(data catalogapp.ComparisonData) ([]conversationapp.AssessmentInput, string) {
	source := data.Source
	sourceTotal, sourceKnown := total(source.Quote)
	if len(data.Candidates) == 0 {
		return nil, fmt.Sprintf("「%s %s」没有可比较的候选规格。先添加候选商品和报价后再试。",
			source.ProductTitle, source.DisplayName)
	}
	assessments := make([]conversationapp.AssessmentInput, 0, len(data.Candidates))
	known := 0
	best := ""
	var bestTotal int64
	for _, candidate := range data.Candidates {
		candidateTotal, candidateKnown := total(candidate.Quote)
		checks := map[string]any{
			"sourcePriceKnown":    sourceKnown,
			"candidatePriceKnown": candidateKnown,
		}
		verdict := "unknown"
		relation := "alternative"
		reason := "候选缺少可用报价，未知价格保持未知。"
		breakdown := map[string]any{
			"sourcePriceFen":       pointerValue(source.Quote.PriceFen),
			"sourceShippingFen":    pointerValue(source.Quote.ShippingFen),
			"sourceTotalFen":       nullableInt(sourceKnown, sourceTotal),
			"candidatePriceFen":    quotePrice(candidate),
			"candidateShippingFen": quoteShipping(candidate),
			"candidateTotalFen":    nullableInt(candidateKnown, candidateTotal),
			"savingFen":            nil,
		}
		if candidateKnown && sourceKnown {
			verdict = "alternative"
			reason = fmt.Sprintf("候选总价 %s 元，源规格 %s 元。", yuan(candidateTotal), yuan(sourceTotal))
			if candidateTotal < sourceTotal {
				breakdown["savingFen"] = sourceTotal - candidateTotal
				reason = fmt.Sprintf("候选总价 %s 元，比源规格便宜 %s 元。", yuan(candidateTotal), yuan(sourceTotal-candidateTotal))
			} else if candidateTotal == sourceTotal {
				breakdown["savingFen"] = int64(0)
				reason = fmt.Sprintf("候选总价与源规格相同（%s 元）。", yuan(candidateTotal))
			} else {
				breakdown["savingFen"] = int64(0)
				reason = fmt.Sprintf("候选总价 %s 元，高于源规格 %s 元。", yuan(candidateTotal), yuan(sourceTotal))
			}
			known++
			if best == "" || candidateTotal < bestTotal {
				best = candidate.ProductTitle + " " + candidate.DisplayName
				bestTotal = candidateTotal
			}
		}
		checksJSON, _ := json.Marshal(checks)
		breakdownJSON, _ := json.Marshal(breakdown)
		assessments = append(assessments, conversationapp.AssessmentInput{
			SourceQuoteID:    quoteID(source.Quote),
			CandidateQuoteID: quoteID(candidate.Quote),
			CandidateSKUID:   stringPtr(candidate.SKUID),
			RelationKind:     relation,
			Verdict:          verdict,
			ConstraintChecks: checksJSON,
			CostBreakdown:    breakdownJSON,
			Reason:           reason,
		})
	}
	var summary string
	if known == 0 {
		summary = fmt.Sprintf("「%s %s」的候选报价不完整，无法计算可比较总价。未知价格和运费保持未知。",
			source.ProductTitle, source.DisplayName)
	} else {
		summary = fmt.Sprintf("已比较 %d 个候选规格，其中 %d 个有完整报价。", len(data.Candidates), known)
		if best != "" {
			summary += fmt.Sprintf("当前最低总价：%s，%s 元。", best, yuan(bestTotal))
		}
		summary += "未知价格没有按 0 处理；下单前请以平台页面为准。"
	}
	return assessments, summary
}

func total(quote *catalogapp.ComparisonQuote) (int64, bool) {
	if quote == nil || quote.PriceFen == nil || quote.ShippingFen == nil {
		return 0, false
	}
	return *quote.PriceFen + *quote.ShippingFen, true
}

func quoteID(quote *catalogapp.ComparisonQuote) *string {
	if quote == nil {
		return nil
	}
	return stringPtr(quote.ID)
}

func quotePrice(item catalogapp.ComparisonItem) any {
	if item.Quote == nil {
		return nil
	}
	return pointerValue(item.Quote.PriceFen)
}

func quoteShipping(item catalogapp.ComparisonItem) any {
	if item.Quote == nil {
		return nil
	}
	return pointerValue(item.Quote.ShippingFen)
}

func pointerValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(known bool, value int64) any {
	if !known {
		return nil
	}
	return value
}

func stringPtr(value string) *string { return &value }

func yuan(fen int64) string {
	return fmt.Sprintf("%d.%02d", fen/100, fen%100)
}
