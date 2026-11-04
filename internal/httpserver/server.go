package httpserver

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	identityhttp "github.com/mdhuser/spendwise-biz/internal/identity/http"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	ledgerhttp "github.com/mdhuser/spendwise-biz/internal/ledger/http"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/httpx"
)

type Server struct {
	cfg             Config
	db              *sql.DB
	logger          *slog.Logger
	identityService *identityapp.Service
	identityHandler *identityhttp.Handler
	ledgerHandler   *ledgerhttp.Handler
}

type Config struct {
	RequestTimeout time.Duration
	Timezone       *time.Location
}

func New(database *sql.DB, logger *slog.Logger, identityService *identityapp.Service, ledgerService *ledgerapp.Service,
	identityHandler *identityhttp.Handler, ledgerHandler *ledgerhttp.Handler, cfg Config) *Server {
	return &Server{
		cfg: cfg, db: database, logger: logger,
		identityService: identityService, identityHandler: identityHandler, ledgerHandler: ledgerHandler,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.db.PingContext(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	auth := s.requireAuth
	s.identityHandler.Register(mux, auth)
	s.ledgerHandler.Register(mux, auth)

	var handler http.Handler = mux
	handler = s.recover(handler)
	handler = s.accessLog(handler)
	handler = s.requestID(handler)
	if s.cfg.RequestTimeout > 0 {
		handler = http.TimeoutHandler(handler, s.cfg.RequestTimeout, `{"error":{"code":"UNAVAILABLE","message":"请求超时"}}`)
	}
	return handler
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			httpx.WriteError(w, r, apperr.Unauthenticated("请先登录"))
			return
		}
		raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		principal, err := s.identityService.Authenticate(r.Context(), raw)
		if err != nil {
			httpx.WriteError(w, r, apperr.Unauthenticated("登录已失效，请重新登录"))
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), *principal)))
	})
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
