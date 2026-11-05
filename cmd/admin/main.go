package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	ledgermysql "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		slog.Error("admin_command_failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 || args[0] != "cleanup" {
		return usageError()
	}
	return runCleanup(args[1:])
}

func usageError() error {
	fmt.Fprintln(os.Stderr, "usage: admin cleanup")
	return flag.ErrHelp
}

func runCleanup(args []string) error {
	flags := flag.NewFlagSet("cleanup", flag.ExitOnError)
	_ = flags.Parse(args)
	if flags.NArg() > 0 {
		return usageError()
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deleted, err := ledgermysql.New(database).DeleteExpiredIdempotency(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("delete idempotency records: %w", err)
	}

	slog.Info("cleanup_completed", "idempotency_records_deleted", deleted)
	fmt.Printf("idempotency_records_deleted=%d\n", deleted)
	return nil
}
