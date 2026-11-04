package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/migrate"
)

func main() {
	cfg, err := config.LoadMigration()
	if err != nil {
		slog.Error("config_error", "error", err)
		os.Exit(1)
	}
	cfg.MySQLDSN = cfg.MigrationDSN
	database, err := db.Open(cfg)
	if err != nil {
		slog.Error("database_error", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := migrate.Apply(ctx, database); err != nil {
		slog.Error("migration_failed", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations_applied")
}
