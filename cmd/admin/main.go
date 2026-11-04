package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	identitymysql "github.com/mdhuser/spendwise-biz/internal/identity/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "invite" || os.Args[2] != "create" {
		fmt.Fprintln(os.Stderr, "usage: admin invite create [-uses 20] [-ttl 720h]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("invite create", flag.ExitOnError)
	uses := flags.Int("uses", 20, "max uses")
	ttl := flags.Duration("ttl", 720*time.Hour, "validity")
	_ = flags.Parse(os.Args[3:])

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config_error", "error", err)
		os.Exit(1)
	}
	database, err := db.Open(cfg)
	if err != nil {
		slog.Error("database_error", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	service := identityapp.New(database, identitymysql.New(database), cfg)
	code, invite, err := service.CreateInvite(context.Background(), *uses, *ttl)
	if err != nil {
		slog.Error("create_invite", "error", err)
		os.Exit(1)
	}
	fmt.Printf("invite_code=%s\nmax_uses=%d\nexpires_at=%s\n", code, invite.MaxUses, invite.ExpiresAt.Format(time.RFC3339))
}
