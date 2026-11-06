// Package llm is the worker-side LLM router skeleton. It preserves the target
// architecture's task routing, total/attempt timeouts and primary->fallback
// behavior without embedding provider SDKs or real keys.
package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type Task string

const (
	TaskExtract Task = "extract"
	TaskSearch  Task = "search"
	TaskDecide  Task = "decide"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client interface {
	Complete(context.Context, Task, []Message) (string, error)
}

type Router struct {
	Primary        Client
	Fallback       Client
	TotalTimeout   time.Duration
	AttemptTimeout time.Duration
	mu             sync.Mutex
	spentFen       int64
	dailyBudgetFen int64
}

func NewRouter(primary, fallback Client, total, attempt time.Duration, dailyBudgetFen int64) *Router {
	if total <= 0 {
		total = 12 * time.Second
	}
	if attempt <= 0 {
		attempt = 5 * time.Second
	}
	return &Router{Primary: primary, Fallback: fallback, TotalTimeout: total, AttemptTimeout: attempt, dailyBudgetFen: dailyBudgetFen}
}

var ErrNoClient = errors.New("llm: no client configured")

// Complete tries the primary then the fallback. Each attempt has a short
// deadline; the whole operation is bounded by TotalTimeout.
func (r *Router) Complete(ctx context.Context, task Task, messages []Message) (string, error) {
	if r == nil {
		return "", ErrNoClient
	}
	ctx, cancel := context.WithTimeout(ctx, r.TotalTimeout)
	defer cancel()
	var lastErr error
	for _, client := range []Client{r.Primary, r.Fallback} {
		if client == nil {
			continue
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, r.AttemptTimeout)
		reply, err := client.Complete(attemptCtx, task, messages)
		attemptCancel()
		if err == nil && strings.TrimSpace(reply) != "" {
			return reply, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = ErrNoClient
	}
	return "", lastErr
}

// FixtureClient is deterministic and useful in tests.
type FixtureClient struct {
	Reply string
	Err   error
}

func (f FixtureClient) Complete(context.Context, Task, []Message) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	return f.Reply, nil
}

// RuleClient is the degradation path: a deterministic template with no model
// call. It never fabricates prices or links.
type RuleClient struct{}

func (RuleClient) Complete(_ context.Context, _ Task, messages []Message) (string, error) {
	lastUser := ""
	for _, message := range messages {
		if message.Role == "user" {
			lastUser = message.Content
		}
	}
	if strings.TrimSpace(lastUser) == "" {
		return "暂未生成建议。", nil
	}
	return "已按你的清单与已知报价完成规则比较。未知价格保持未知，请以平台页面为准。", nil
}

// BudgetRemaining reports the remaining daily budget in fen. A zero budget
// means "unlimited" for local/fixture runs.
func (r *Router) BudgetRemaining() int64 {
	if r == nil || r.dailyBudgetFen <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	remaining := r.dailyBudgetFen - r.spentFen
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RecordCostFen is called by the worker after usage is known. Fixture runs use
// zero; real clients pass provider usage.
func (r *Router) RecordCostFen(costFen int64) {
	if r == nil || costFen <= 0 {
		return
	}
	r.mu.Lock()
	r.spentFen += costFen
	r.mu.Unlock()
}
