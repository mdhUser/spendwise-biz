package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

type Handler struct {
	service *ledgerapp.Service
	loc     *time.Location
}

func New(service *ledgerapp.Service, loc *time.Location) *Handler {
	return &Handler{service: service, loc: loc}
}

func (h *Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/ledger/accounts", auth(http.HandlerFunc(h.listAccounts)))
	mux.Handle("POST /v1/ledger/accounts", auth(http.HandlerFunc(h.createAccount)))
	mux.Handle("PATCH /v1/ledger/accounts/{id}", auth(http.HandlerFunc(h.updateAccount)))
	mux.Handle("DELETE /v1/ledger/accounts/{id}", auth(http.HandlerFunc(h.archiveAccount)))

	mux.Handle("GET /v1/ledger/categories", auth(http.HandlerFunc(h.listCategories)))
	mux.Handle("POST /v1/ledger/categories", auth(http.HandlerFunc(h.createCategory)))
	mux.Handle("PATCH /v1/ledger/categories/{id}", auth(http.HandlerFunc(h.updateCategory)))
	mux.Handle("DELETE /v1/ledger/categories/{id}", auth(http.HandlerFunc(h.archiveCategory)))

	mux.Handle("GET /v1/ledger/transactions", auth(http.HandlerFunc(h.listTransactions)))
	mux.Handle("POST /v1/ledger/transactions", auth(http.HandlerFunc(h.createTransaction)))
	mux.Handle("GET /v1/ledger/transactions/{id}", auth(http.HandlerFunc(h.getTransaction)))
	mux.Handle("PATCH /v1/ledger/transactions/{id}", auth(http.HandlerFunc(h.updateTransaction)))
	mux.Handle("DELETE /v1/ledger/transactions/{id}", auth(http.HandlerFunc(h.deleteTransaction)))
	mux.Handle("POST /v1/ledger/transactions/{id}/restore", auth(http.HandlerFunc(h.restoreTransaction)))
	mux.Handle("GET /v1/ledger/summary", auth(http.HandlerFunc(h.summary)))

	mux.Handle("GET /v1/ledger/saving-goals", auth(http.HandlerFunc(h.listGoals)))
	mux.Handle("POST /v1/ledger/saving-goals", auth(http.HandlerFunc(h.createGoal)))
	mux.Handle("GET /v1/ledger/saving-goals/overview", auth(http.HandlerFunc(h.overview)))
	mux.Handle("GET /v1/ledger/saving-goals/{id}", auth(http.HandlerFunc(h.getGoal)))
	mux.Handle("PATCH /v1/ledger/saving-goals/{id}", auth(http.HandlerFunc(h.updateGoal)))
	mux.Handle("DELETE /v1/ledger/saving-goals/{id}", auth(http.HandlerFunc(h.archiveGoal)))
	mux.Handle("POST /v1/ledger/saving-goals/{id}/terms", auth(http.HandlerFunc(h.addTerm)))
	mux.Handle("GET /v1/ledger/saving-goals/{id}/achievements", auth(http.HandlerFunc(h.achievements)))
}

func principal(r *http.Request) (identity.Principal, bool) {
	return identity.PrincipalFromContext(r.Context())
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.ListAccounts(r.Context(), p.User.ID, boolQuery(r, "includeArchived"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input ledgerapp.CreateAccountInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateAccount(r.Context(), p.User.ID, p.User.ID, httpx.RequestID(r.Context()), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, http.StatusCreated, locationFor(body, "/v1/ledger/accounts/"))
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input ledgerapp.UpdateAccountInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateAccount(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context()), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) archiveAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.ArchiveAccount(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.ListCategories(r.Context(), p.User.ID, r.URL.Query().Get("kind"), boolQuery(r, "includeArchived"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input ledgerapp.CreateCategoryInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateCategory(r.Context(), p.User.ID, p.User.ID, httpx.RequestID(r.Context()), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, http.StatusCreated, locationFor(body, "/v1/ledger/categories/"))
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input ledgerapp.UpdateCategoryInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateCategory(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context()), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) archiveCategory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.ArchiveCategory(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	from, to, err := h.dateRange(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	filter := domain.TransactionFilter{From: &from, To: &to, Limit: limitQuery(r)}
	if value := r.URL.Query().Get("type"); value != "" {
		filter.Type = domain.TransactionType(value)
	}
	if value := r.URL.Query().Get("categoryId"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			httpx.WriteError(w, r, httpx.New(422, "VALIDATION_FAILED", "categoryId 不合法"))
			return
		}
		filter.CategoryID = &id
	}
	if value := r.URL.Query().Get("accountId"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			httpx.WriteError(w, r, httpx.New(422, "VALIDATION_FAILED", "accountId 不合法"))
			return
		}
		filter.AccountID = &id
	}
	filter.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	filter.Cursor = r.URL.Query().Get("cursor")
	body, err := h.service.ListTransactions(r.Context(), p.User.ID, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input ledgerapp.CreateTransactionInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateTransaction(r.Context(), p.User.ID, p.User.ID, httpx.RequestID(r.Context()), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, http.StatusCreated, locationFor(body, "/v1/ledger/transactions/"))
}

func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	body, err := h.service.GetTransaction(r.Context(), p.User.ID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) updateTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input ledgerapp.UpdateTransactionInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateTransaction(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context()), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) deleteTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteTransaction(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) restoreTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.RestoreTransaction(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	from, to, err := h.dateRange(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "category"
	}
	if groupBy != "category" && groupBy != "month" && groupBy != "day" {
		httpx.WriteError(w, r, httpx.New(422, "VALIDATION_FAILED", "groupBy 必须是 category/month/day"))
		return
	}
	body, err := h.service.Summary(r.Context(), p.User.ID, from, to, groupBy)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) listGoals(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	body, err := h.service.ListGoals(r.Context(), p.User.ID, r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) createGoal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var input ledgerapp.CreateGoalInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateGoal(r.Context(), p.User.ID, p.User.ID, httpx.RequestID(r.Context()), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, http.StatusCreated, locationFor(body, "/v1/ledger/saving-goals/"))
}

func (h *Handler) getGoal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	body, err := h.service.GetGoal(r.Context(), p.User.ID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) updateGoal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input ledgerapp.UpdateGoalInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.UpdateGoal(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context()), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) archiveGoal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.ArchiveGoal(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addTerm(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input ledgerapp.AddTermInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.AddTerm(r.Context(), p.User.ID, p.User.ID, id, httpx.RequestID(r.Context()), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeMutation(w, body, replay, http.StatusCreated, "")
}

func (h *Handler) achievements(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	from, to, err := h.periodRange(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.Achievements(r.Context(), p.User.ID, id, from, to)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(r)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	period, err := h.periodQuery(r, "period", domain.PeriodFromTime(time.Now().UTC(), h.loc))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := h.service.Overview(r.Context(), p.User.ID, period)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) dateRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().In(h.loc)
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, h.loc)
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc).AddDate(0, 0, 1)
	if value := strings.TrimSpace(r.URL.Query().Get("from")); value != "" {
		parsed, err := domain.ParseDate(value, h.loc)
		if err != nil {
			return time.Time{}, time.Time{}, httpx.New(422, "VALIDATION_FAILED", "from 必须是 YYYY-MM-DD")
		}
		from = parsed
	}
	if value := strings.TrimSpace(r.URL.Query().Get("to")); value != "" {
		parsed, err := domain.ParseDate(value, h.loc)
		if err != nil {
			return time.Time{}, time.Time{}, httpx.New(422, "VALIDATION_FAILED", "to 必须是 YYYY-MM-DD")
		}
		to = parsed.AddDate(0, 0, 1)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, httpx.New(422, "VALIDATION_FAILED", "from 必须早于 to")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, httpx.New(422, "VALIDATION_FAILED", "日期范围不能超过 366 天")
	}
	return from.UTC(), to.UTC(), nil
}

func (h *Handler) periodRange(r *http.Request) (domain.Period, domain.Period, error) {
	current := domain.PeriodFromTime(time.Now().UTC(), h.loc)
	from := current.AddMonths(-11)
	to := current
	if value := strings.TrimSpace(r.URL.Query().Get("from")); value != "" {
		parsed, err := domain.ParsePeriod(value)
		if err != nil {
			return domain.Period{}, domain.Period{}, httpx.New(422, "VALIDATION_FAILED", "from 必须是 YYYY-MM")
		}
		from = parsed
	}
	if value := strings.TrimSpace(r.URL.Query().Get("to")); value != "" {
		parsed, err := domain.ParsePeriod(value)
		if err != nil {
			return domain.Period{}, domain.Period{}, httpx.New(422, "VALIDATION_FAILED", "to 必须是 YYYY-MM")
		}
		to = parsed
	}
	if to.After(current) {
		to = current
	}
	if from.After(to) {
		return domain.Period{}, domain.Period{}, httpx.New(422, "VALIDATION_FAILED", "from 不能晚于 to")
	}
	if len(domain.PeriodsBetween(from, to)) > 36 {
		return domain.Period{}, domain.Period{}, httpx.New(422, "VALIDATION_FAILED", "查询范围最多 36 个月")
	}
	return from, to, nil
}

func (h *Handler) periodQuery(r *http.Request, key string, fallback domain.Period) (domain.Period, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback, nil
	}
	period, err := domain.ParsePeriod(value)
	if err != nil {
		return domain.Period{}, httpx.New(422, "VALIDATION_FAILED", key+" 必须是 YYYY-MM")
	}
	return period, nil
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	value := r.PathValue("id")
	id, err := uuid.Parse(value)
	if err != nil {
		httpx.WriteError(w, r, httpx.New(422, "VALIDATION_FAILED", "id 不合法"))
		return uuid.Nil, false
	}
	return id, true
}

func boolQuery(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}

func limitQuery(r *http.Request) int {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

func locationFor(body any, prefix string) string {
	if values, ok := body.(map[string]any); ok {
		if id, ok := values["id"].(uuid.UUID); ok {
			return prefix + id.String()
		}
	}
	return ""
}

func writeMutation(w http.ResponseWriter, body any, replay bool, status int, location string) {
	if location != "" {
		w.Header().Set("Location", location)
	}
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	httpx.WriteJSON(w, status, body)
}
