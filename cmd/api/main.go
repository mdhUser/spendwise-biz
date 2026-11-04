package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/httpserver"
	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	identityhttp "github.com/mdhuser/spendwise-biz/internal/identity/http"
	identitymysql "github.com/mdhuser/spendwise-biz/internal/identity/store/mysql"
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

	identityStore := identitymysql.New(database)
	identityService := identityapp.New(database, identityStore, cfg)
	ledgerStore := ledgermysql.New(database)
	ledgerService := ledgerapp.New(database, ledgerStore, cfg)

	identityHandler := identityhttp.New(identityService)
	identityHandler.SetDefaults(func(ctx context.Context, userID uuid.UUID) error {
		return ledgerService.EnsureDefaults(ctx, userID)
	})
	identityHandler.SetPurge(func(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error {
		return ledgerService.PurgeUser(ctx, tx, userID)
	})
	ledgerHandler := ledgerhttp.New(ledgerService, cfg.Location())

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpserver.New(
			database, logger, identityService, ledgerService, identityHandler, ledgerHandler,
			httpserver.Config{RequestTimeout: cfg.RequestTimeout, Timezone: cfg.Location()},
		).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	logger.Info("server_started", "addr", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server_error", "error", err)
		os.Exit(1)
	}
}
