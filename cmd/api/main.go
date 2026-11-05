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

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpserver.New(
			database, logger, ledgerService, ledgerHandler, authClient,
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

	logger.Info("server_started", "addr", cfg.HTTPAddr, "auth_base_url", cfg.AuthBaseURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server_error", "error", err)
		os.Exit(1)
	}
	// Shutdown closes the listener first, so ListenAndServe returns before
	// in-flight handlers finish. Wait for Shutdown to drain them before the
	// deferred database.Close runs.
	<-shutdownDone
}
