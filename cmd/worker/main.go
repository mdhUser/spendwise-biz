package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	catalogapp "github.com/mdhuser/spendwise-biz/internal/catalog/app"
	catalogmysql "github.com/mdhuser/spendwise-biz/internal/catalog/store/mysql"
	conversationapp "github.com/mdhuser/spendwise-biz/internal/conversation/app"
	conversationmysql "github.com/mdhuser/spendwise-biz/internal/conversation/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/jobs"
	"github.com/mdhuser/spendwise-biz/internal/jobs/handlers"
	"github.com/mdhuser/spendwise-biz/internal/llm"
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

	workerID := cfg.WorkerID
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = host + "-" + time.Now().UTC().Format("150405")
	}
	queue := jobs.NewQueue(database, workerID)
	catalogStore := catalogmysql.New(database)
	catalogService := catalogapp.New(catalogStore)
	conversationStore := conversationmysql.New(database)
	conversationService := conversationapp.New(conversationStore)

	router := llm.NewRouter(llm.RuleClient{}, nil, cfg.LLMTotalTimeout, cfg.LLMAttemptTimeout, cfg.LLMDailyBudgetFen)
	handler := &handlers.RunSubstitution{
		Conversations: conversationService,
		Catalog:       catalogService,
		LLM:           router,
		Logger:        logger,
	}
	worker := jobs.NewWorker(queue, handler.Handle, logger)
	worker.PollInterval = cfg.WorkerPoll
	worker.HeartbeatInterval = cfg.WorkerHeartbeat
	worker.RunTimeout = cfg.RunTimeout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if count, err := queue.RequeueExpired(ctx); err != nil {
					logger.Warn("jobs_requeue_failed", "error", err)
				} else if count > 0 {
					logger.Info("jobs_requeued", "count", count)
				}
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("worker_stopping", "worker_id", workerID)
		cancel()
	}()

	// Each job has a hard 60s budget; the worker still waits for the handler to
	// return so the queue lock can be released cleanly.
	logger.Info("worker_started", "worker_id", workerID, "poll", cfg.WorkerPoll, "heartbeat", cfg.WorkerHeartbeat)
	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("worker_error", "error", err)
		os.Exit(1)
	}
	logger.Info("worker_stopped")
}
