package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/authclient"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	ledgerhttp "github.com/mdhuser/spendwise-biz/internal/ledger/http"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

// Authenticator is the legacy opaque-token adapter. It is only consulted when
// local JWT verification fails.
type Authenticator interface {
	Authenticate(context.Context, string) (authclient.User, error)
}

// AccessVerifier verifies Ed25519 access tokens with a cached JWKS.
type AccessVerifier interface {
	Verify(context.Context, string) (identity.Principal, error)
	Ready() bool
	Ensure(context.Context) error
}

// Registrar lets a business module register its routes without leaking its
// concrete handler type into the server assembly.
type Registrar interface {
	Register(*http.ServeMux, func(http.Handler) http.Handler)
}

type Server struct {
	cfg           Config
	db            *sql.DB
	logger        *slog.Logger
	ledgerService *ledgerapp.Service
	ledgerHandler *ledgerhttp.Handler
	authClient    Authenticator
	verifier      AccessVerifier
	modules       []Registrar
	defaultsReady sync.Map
}

type Config struct {
	RequestTimeout  time.Duration
	Timezone        *time.Location
	AuthCookieName  string
	PlainCookieName string
}

func New(database *sql.DB, logger *slog.Logger, ledgerService *ledgerapp.Service, ledgerHandler *ledgerhttp.Handler,
	authClient Authenticator, verifier AccessVerifier, cfg Config, modules ...Registrar) *Server {
	return &Server{
		cfg: cfg, db: database, logger: logger,
		ledgerService: ledgerService, ledgerHandler: ledgerHandler,
		authClient: authClient, verifier: verifier, modules: modules,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.verifier != nil && !s.verifier.Ready() {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			err := s.verifier.Ensure(ctx)
			cancel()
			if err != nil || !s.verifier.Ready() {
				s.logger.Warn("readiness_jwks_not_ready", "error", err)
				httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "jwks"})
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
			return
		}
		var version int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1`,
		).Scan(&version); err != nil || version == 0 {
			s.logger.Warn("readiness_migration_check_failed", "error", err, "version", version)
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	if s.ledgerHandler != nil {
		s.ledgerHandler.Register(mux, s.requireAuth)
	}
	for _, module := range s.modules {
		if module != nil {
			module.Register(mux, s.requireAuth)
		}
	}

	var handler http.Handler = mux
	handler = s.recover(handler)
	handler = s.accessLog(handler)
	handler = s.requestID(handler)
	if s.cfg.RequestTimeout > 0 {
		// SSE streams are long-lived and must not be wrapped by TimeoutHandler.
		inner := handler
		timed := http.TimeoutHandler(inner, s.cfg.RequestTimeout, `{"error":{"code":"UNAVAILABLE","message":"请求超时"}}`)
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/events") {
				inner.ServeHTTP(w, r)
				return
			}
			timed.ServeHTTP(w, r)
		})
	}
	return handler
}

// requireAuth verifies the access token locally first (Ed25519 JWT + cached
// JWKS). Only when that fails does it fall back to the legacy opaque
// /v1/auth/me call. The auth service is the only identity provider: biz never
// reads auth users/sessions tables. A rejected token becomes 401; a transport
// or auth 5xx failure on the fallback becomes 503.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			for _, name := range []string{s.cfg.AuthCookieName, s.cfg.PlainCookieName} {
				if name == "" {
					continue
				}
				if cookie, err := r.Cookie(name); err == nil && cookie.Value != "" {
					token = cookie.Value
					break
				}
			}
		}
		if token == "" {
			httpx.WriteError(w, r, apperr.Unauthenticated("请先登录"))
			return
		}

		if s.verifier != nil {
			principal, err := s.verifier.Verify(r.Context(), token)
			if err == nil {
				user := authclient.User{ID: principal.ID, UserType: "registered", Status: "active"}
				if principal.Name != "" {
					name := principal.Name
					user.DisplayName = &name
				}
				w.Header().Set("X-Auth-Method", "jwt")
				s.ensureDefaults(r.Context(), user.ID)
				ctx := authclient.WithUser(r.Context(), user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			s.logger.Debug("jwt_verify_failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		}

		if s.authClient == nil {
			httpx.WriteError(w, r, apperr.Unauthenticated("登录已失效，请重新登录"))
			return
		}
		user, err := s.authClient.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, authclient.ErrUnauthenticated) {
				httpx.WriteError(w, r, apperr.Unauthenticated("登录已失效，请重新登录"))
				return
			}
			s.logger.Warn("auth_unavailable", "request_id", httpx.RequestID(r.Context()), "error", err)
			httpx.WriteError(w, r, apperr.New(http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "认证服务暂时不可用，请稍后再试"))
			return
		}
		w.Header().Set("X-Auth-Method", "opaque")
		s.ensureDefaults(r.Context(), user.ID)
		next.ServeHTTP(w, r.WithContext(authclient.WithUser(r.Context(), user)))
	})
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 7 || !strings.EqualFold(header[:6], "Bearer") || header[6] != ' ' {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

// ensureDefaults creates the starter accounts/categories once per user per
// process. It is best effort: a failure is logged and retried on the next
// request, but it never turns an otherwise valid login into an error.
func (s *Server) ensureDefaults(ctx context.Context, ownerID string) {
	if s.ledgerService == nil {
		return
	}
	if _, ok := s.defaultsReady.Load(ownerID); ok {
		return
	}
	if err := s.ledgerService.EnsureDefaults(ctx, ownerID); err != nil {
		s.logger.Warn("ensure_defaults_failed", "owner_id", ownerID, "error", err)
		return
	}
	s.defaultsReady.Store(ownerID, struct{}{})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if requestID == "" || len(requestID) > 64 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", requestID)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), requestID)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush preserves http.Flusher through the access-log wrapper so SSE works.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(recorder, r)
		s.logger.Info("http_request",
			"request_id", httpx.RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("request_panic", "request_id", httpx.RequestID(r.Context()), "panic", value)
				httpx.WriteError(w, r, apperr.New(500, "INTERNAL", "服务暂时不可用，请稍后再试"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
