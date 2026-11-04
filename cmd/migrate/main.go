package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/migrations"
	"github.com/pressly/goose/v3"
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

	conn, err := database.Conn(ctx)
	if err != nil {
		slog.Error("migration_lock_connection", "error", err)
		os.Exit(1)
	}
	defer conn.Close()
	var acquired int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('spendwise_biz_migrations', 30)`).Scan(&acquired); err != nil || acquired != 1 {
		slog.Error("migration_lock_failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK('spendwise_biz_migrations')`)
	}()

	if err := goose.SetDialect("mysql"); err != nil {
		slog.Error("goose_dialect", "error", err)
		os.Exit(1)
	}
	for _, module := range []string{"identity", "ledger"} {
		sub, err := fs.Sub(migrations.FS, module)
		if err != nil {
			slog.Error("migration_fs", "module", module, "error", err)
			os.Exit(1)
		}
		goose.SetBaseFS(sub)
		if err := goose.UpContext(ctx, database, "."); err != nil {
			slog.Error("migration_failed", "module", module, "error", err)
			os.Exit(1)
		}
	}
	slog.Info("migrations_applied")
}
