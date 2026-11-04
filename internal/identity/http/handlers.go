package http

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

type Handler struct {
	service *identityapp.Service
	purge   func(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error
}

func New(service *identityapp.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) SetPurge(fn func(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error) {
	h.purge = fn
}

func (h *Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /v1/auth/register", h.register)
	mux.HandleFunc("POST /v1/auth/login", h.login)
	mux.Handle("POST /v1/auth/logout", auth(http.HandlerFunc(h.logout)))
	mux.Handle("GET /v1/auth/me", auth(http.HandlerFunc(h.me)))
	mux.Handle("POST /v1/auth/password", auth(http.HandlerFunc(h.password)))
	mux.Handle("DELETE /v1/auth/account", auth(http.HandlerFunc(h.deleteAccount)))
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	InviteCode  string `json:"inviteCode"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := h.service.Register(r.Context(), identityapp.RegisterInput{
		Email: req.Email, Password: req.Password, DisplayName: req.DisplayName, InviteCode: req.InviteCode,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, authResponse(result))
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := h.service.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) && appErr.Code == "ACCOUNT_LOCKED" {
			w.Header().Set("Retry-After", "900")
		}
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, authResponse(result))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	raw := bearerToken(r)
	if raw != "" {
		_ = h.service.Logout(r.Context(), raw)
	}
	httpx.WriteNoContent(w)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": principal.User.ID, "email": principal.User.Email,
			"displayName": principal.User.DisplayName, "createdAt": displayTime(principal.User.CreatedAt),
		},
		"session": map[string]any{
			"expiresAt": displayTime(principal.Session.ExpiresAt), "lastSeenAt": displayTime(principal.Session.LastSeenAt),
		},
	})
}

type passwordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (h *Handler) password(w http.ResponseWriter, r *http.Request) {
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var req passwordRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.ChangePassword(r.Context(), principal, req.CurrentPassword, req.NewPassword); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}

func authResponse(result *identityapp.AuthResult) map[string]any {
	return map[string]any{
		"user": map[string]any{
			"id": result.User.ID, "email": result.User.Email,
			"displayName": result.User.DisplayName, "createdAt": displayTime(result.User.CreatedAt),
		},
		"token": result.Token, "expiresAt": displayTime(result.ExpiresAt),
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

var displayLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

func displayTime(value time.Time) string {
	return value.In(displayLocation).Format(time.RFC3339)
}

type deleteAccountRequest struct {
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	principal, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录"))
		return
	}
	var req deleteAccountRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.DeleteAccount(r.Context(), principal, req.Password, req.Confirm, h.purge); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteNoContent(w)
}
