package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	conversationapp "github.com/mdhuser/spendwise-biz/internal/conversation/app"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

func (h *Handler) createRun(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	if !h.allow(w, r, "run:"+owner, 60, 24*time.Hour) {
		return
	}
	var input conversationapp.CreateRunInput
	if err := httpx.Decode(w, r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, replay, err := h.service.CreateRun(r.Context(), owner, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/runs/"+body.ID)
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, body)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, body)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	body, err := h.service.GetRun(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	body, err := h.service.CancelRun(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// runEvents streams snapshot -> progress -> result. It polls the run row every
// 300ms (the target architecture's v1 approach) and heartbeats every 15s. A
// client disconnect only stops the stream; cancellation is explicit.
func (h *Handler) runEvents(w http.ResponseWriter, r *http.Request) {
	owner, ok := ownerID(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusInternalServerError, "SSE_UNSUPPORTED", "当前连接不支持流式响应"))
		return
	}
	runID := r.PathValue("id")
	run, err := h.service.GetRun(r.Context(), owner, runID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	if !writeEvent(w, flusher, "snapshot", run) {
		return
	}
	if terminal(run.Status) {
		_ = writeEvent(w, flusher, "result", run)
		return
	}

	poll := time.NewTicker(300 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	started := time.Now()
	lastStatus := run.Status
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			current, err := h.service.GetRun(r.Context(), owner, runID)
			if err != nil {
				_ = writeEvent(w, flusher, "error", map[string]any{"runId": runID, "message": "运行状态不可用"})
				return
			}
			if current.Status != lastStatus {
				lastStatus = current.Status
				if terminal(current.Status) {
					_ = writeEvent(w, flusher, "result", current)
					return
				}
			}
			progress := map[string]any{
				"runId": current.ID, "status": current.Status,
				"elapsedMs": time.Since(started).Milliseconds(),
			}
			if !writeEvent(w, flusher, "progress", progress) {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}
