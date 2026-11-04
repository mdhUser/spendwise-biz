package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	identitymysql "github.com/mdhuser/spendwise-biz/internal/identity/store/mysql"
	ledgermysql "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

const defaultSessionRetention = 30 * 24 * time.Hour

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
	if len(args) < 1 {
		return usageError()
	}
	switch args[0] {
	case "invite":
		if len(args) < 2 || args[1] != "create" {
			return usageError()
		}
		return runInviteCreate(args[2:])
	case "cleanup":
		return runCleanup(args[1:])
	default:
		return usageError()
	}
}

func usageError() error {
	fmt.Fprintln(os.Stderr, "usage: admin invite create [-uses 20] [-ttl 720h]")
	fmt.Fprintln(os.Stderr, "       admin cleanup [-session-retention 720h]")
	return flag.ErrHelp
}

func runInviteCreate(args []string) error {
	flags := flag.NewFlagSet("invite create", flag.ExitOnError)
	uses := flags.Int("uses", 20, "max uses")
	ttl := flags.Duration("ttl", 720*time.Hour, "validity")
	_ = flags.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer database.Close()

	service := identityapp.New(database, identitymysql.New(database), cfg)
	code, invite, err := service.CreateInvite(context.Background(), *uses, *ttl)
	if err != nil {
		return fmt.Errorf("create invite: %w", err)
	}
	fmt.Printf("invite_code=%s\nmax_uses=%d\nexpires_at=%s\n", code, invite.MaxUses, invite.ExpiresAt.Format(time.RFC3339))
	return nil
}

func runCleanup(args []string) error {
	flags := flag.NewFlagSet("cleanup", flag.ExitOnError)
	retention := flags.Duration("session-retention", defaultSessionRetention, "delete sessions expired or revoked longer ago than this")
	_ = flags.Parse(args)
	if *retention < 0 {
		return fmt.Errorf("session-retention must not be negative")
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
	now := time.Now().UTC()

	sessions, err := identitymysql.New(database).DeleteExpiredSessions(ctx, now, *retention)
	if err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	idempotency, err := ledgermysql.New(database).DeleteExpiredIdempotency(ctx, now)
	if err != nil {
		return fmt.Errorf("delete idempotency records: %w", err)
	}

	slog.Info("cleanup_completed", "sessions_deleted", sessions,
		"idempotency_records_deleted", idempotency, "session_retention", retention.String())
	fmt.Printf("sessions_deleted=%d\nidempotency_records_deleted=%d\n", sessions, idempotency)
	return nil
}
