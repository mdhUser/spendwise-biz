package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mdhuser/spendwise-biz/internal/authclient"
	conversationapp "github.com/mdhuser/spendwise-biz/internal/conversation/app"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
	"github.com/mdhuser/spendwise-biz/internal/platform/ratelimit"
)

type Handler struct {
	service *conversationapp.Service
	limiter *ratelimit.Limiter
}

func New(service *conversationapp.Service, limiter *ratelimit.Limiter) *Handler {
	return &Handler{service: service, limiter: limiter}
}

func (h *Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/conversations", auth(http.HandlerFunc(h.list)))
	mux.Handle("POST /v1/conversations", auth(http.HandlerFunc(h.create)))
	mux.Handle("DELETE /v1/conversations", auth(http.HandlerFunc(h.deleteAll)))
	mux.Handle("GET /v1/conversations/{id}", auth(http.HandlerFunc(h.get)))
	mux.Handle("DELETE /v1/conversations/{id}", auth(http.HandlerFunc(h.delete)))
	mux.Handle("GET /v1/conversations/{id}/messages", auth(http.HandlerFunc(h.listMessages)))
	mux.Handle("POST /v1/conversations/{id}/messages", auth(http.HandlerFunc(h.createMessage)))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	body, err := h.service.List(r.Context(), owner, r.URL.Query().Get("cursor"), limitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	if !h.allow(w, r, "new:"+owner, 30, 24*time.Hour) {
		return
	}
	var input conversationapp.CreateInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.Create(r.Context(), owner, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	w.Header().Set("Location", "/v1/conversations/"+body.ID)
	httpx.WriteJSON(w, http.StatusCreated, body)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	body, err := h.service.Get(r.Context(), owner, r.PathValue("id"), messageLimitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	if err := h.service.Delete(r.Context(), owner, r.PathValue("id")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) deleteAll(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	if err := h.service.DeleteAll(r.Context(), owner); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	items, err := h.service.ListMessages(r.Context(), owner, r.PathValue("id"), messageLimitQuery(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createMessage(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	if !h.allow(w, r, "msg:"+owner, 120, 24*time.Hour) {
		return
	}
	var input conversationapp.MessageInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateMessage(r.Context(), owner, r.PathValue("id"), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	w.Header().Set("Location", "/v1/conversations/"+body.ConversationID+"/messages/"+body.ID)
	httpx.WriteJSON(w, http.StatusCreated, body)
}

// allow applies the fixed-window limiter. A limiter/database error fails open:
// availability of the core write path is more important than a missed window,
// and the error is still surfaced through request logs.
func (h *Handler) allow(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	if h.limiter == nil {
		return true
	}
	decision, err := h.limiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		return true
	}
	w.Header().Set("RateLimit-Limit", strconv.Itoa(decision.Limit))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	w.Header().Set("RateLimit-Reset", strconv.FormatInt(decision.Reset.Unix(), 10))
	if decision.Allowed {
		return true
	}
	retryAfter := int(time.Until(decision.Reset).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	httpx.WriteError(w, r, apperr.New(http.StatusTooManyRequests, "RATE_LIMITED", "操作太频繁，请稍后再试"))
	return false
}

func ownerID(r *http.Request) (string, bool) {
	user, ok := authclient.UserFromContext(r.Context())
	if !ok || strings.TrimSpace(user.ID) == "" {
		return "", false
	}
	return user.ID, true
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, apperr.Unauthenticated("请先登录"))
}

func limitQuery(r *http.Request) int {
	return intQuery(r, "limit", 50)
}

func messageLimitQuery(r *http.Request) int {
	return intQuery(r, "limit", 100)
}

func intQuery(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	if parsed > 100 {
		return 100
	}
	return parsed
}
