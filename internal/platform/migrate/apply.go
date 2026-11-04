// Package migrate applies the embedded goose migrations.
//
// It is shared by cmd/migrate and by MySQL integration tests so both exercise
// exactly the same ordering, advisory lock and error handling.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/mdhuser/spendwise-biz/migrations"
	"github.com/pressly/goose/v3"
)

// LockName is the MySQL advisory lock used to serialize concurrent migration
// runs across processes/instances.
const LockName = "spendwise_biz_migrations"

// Modules lists migration directories in dependency order. identity must run
// before ledger because ledger tables have foreign keys to users.
var Modules = []string{"identity", "ledger", "shared"}

// Apply runs every pending goose migration up to the latest version.
//
// It holds the GET_LOCK advisory lock on a dedicated connection for the whole
// run. MySQL DDL is not transactional, so the ordering and the lock are the
// only guarantees; a failure is reported with the module that failed and the
// caller is expected to fix forward or restore from backup.
func Apply(ctx context.Context, database *sql.DB) error {
	conn, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration lock connection: %w", err)
	}
	defer conn.Close()

	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 30)`, LockName).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("acquire migration lock %q: timed out or failed", LockName)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(releaseCtx, `SELECT RELEASE_LOCK(?)`, LockName)
	}()

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	for _, module := range Modules {
		sub, err := fs.Sub(migrations.FS, module)
		if err != nil {
			return fmt.Errorf("migration files for module %s: %w", module, err)
		}
		goose.SetBaseFS(sub)
		if err := goose.UpContext(ctx, database, "."); err != nil {
			return fmt.Errorf("migration failed for module %s: %w", module, err)
		}
	}
	return nil
}
