package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/mdhuser/spendwise-biz/internal/authclient"
	"github.com/mdhuser/spendwise-biz/internal/httpserver"
	"github.com/mdhuser/spendwise-biz/internal/identity"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	ledgerhttp "github.com/mdhuser/spendwise-biz/internal/ledger/http"
	ledgermysql "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config_error", "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	database, err := db.Open(cfg)
	if err != nil {
		logger.Error("database_error", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ledgerStore := ledgermysql.New(database)
	ledgerService := ledgerapp.New(database, ledgerStore, cfg)
	ledgerHandler := ledgerhttp.New(ledgerService, cfg.Location())
	authClient := authclient.New(cfg.AuthBaseURL, cfg.AuthTimeout)
	verifier := identity.NewVerifier(identity.Config{
		JWKSURL:         cfg.AuthJWKSURL,
		Issuer:          cfg.AuthIssuer,
		Audience:        cfg.AuthAudience,
		CacheTTL:        cfg.AuthJWKSCacheTTL,
		HTTPTimeout:     cfg.AuthTimeout,
		StaticPublicKey: cfg.AuthStaticPublicKey,
		StaticKeyID:     cfg.AuthStaticKeyID,
	})
	primeCtx, cancelPrime := context.WithTimeout(context.Background(), 5*time.Second)
	primeErr := verifier.Prime(primeCtx)
	cancelPrime()
	if primeErr != nil {
		// Do not exit: /readyz will stay 503 until JWKS loads, and an embedded
		// static key can keep verification available during an auth outage.
		logger.Warn("jwks_prime_failed", "url", cfg.AuthJWKSURL, "error", primeErr)
	} else {
		logger.Info("jwks_loaded", "url", cfg.AuthJWKSURL)
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpserver.New(
			database, logger, ledgerService, ledgerHandler, authClient, verifier,
			httpserver.Config{
				RequestTimeout:  cfg.RequestTimeout,
				Timezone:        cfg.Location(),
				AuthCookieName:  cfg.AuthAccessCookieName,
				PlainCookieName: cfg.AuthPlainCookieName,
			},
		).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("server_shutdown", "error", err)
		}
	}()

	logger.Info("server_started", "addr", cfg.HTTPAddr, "auth_base_url", cfg.AuthBaseURL, "jwks_url", cfg.AuthJWKSURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server_error", "error", err)
		os.Exit(1)
	}
	// Shutdown closes the listener first, so ListenAndServe returns before
	// in-flight handlers finish. Wait for Shutdown to drain them before the
	// deferred database.Close runs.
	<-shutdownDone
}
