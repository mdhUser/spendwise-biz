package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
)

func Open(cfg *config.Config) (*sql.DB, error) {
	dsnCfg, err := mysql.ParseDSN(cfg.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("parse mysql dsn: %w", err)
	}
	dsnCfg.ParseTime = true
	dsnCfg.Loc = time.UTC
	dsnCfg.MultiStatements = false
	dsnCfg.InterpolateParams = false
	if dsnCfg.Params == nil {
		dsnCfg.Params = map[string]string{}
	}
	// DATETIME is timezone-less; keep every session in UTC.
	dsnCfg.Params["time_zone"] = "'+00:00'"
	delete(dsnCfg.Params, "charset")
	if dsnCfg.Collation == "" {
		dsnCfg.Collation = "utf8mb4_0900_ai_ci"
	}

	connector, err := mysql.NewConnector(dsnCfg)
	if err != nil {
		return nil, fmt.Errorf("mysql connector: %w", err)
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(cfg.DBMaxOpen)
	database.SetMaxIdleConns(cfg.DBMaxIdle)
	database.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	database.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.DBTimeout)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return database, nil
}

func WithTx(ctx context.Context, database *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	tx, err := database.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func IsDuplicate(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func IsForeignKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1451 || mysqlErr.Number == 1452)
}

func IsCheckViolation(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 3819
}

func IsDeadlock(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func UUIDBytes(id uuid.UUID) []byte {
	b := make([]byte, 16)
	copy(b, id[:])
	return b
}

func UUIDFromBytes(b []byte) (uuid.UUID, error) {
	if len(b) != 16 {
		return uuid.Nil, fmt.Errorf("invalid uuid bytes length %d", len(b))
	}
	var id uuid.UUID
	copy(id[:], b)
	return id, nil
}

func UUIDPtrFromBytes(b []byte) (*uuid.UUID, error) {
	if b == nil {
		return nil, nil
	}
	id, err := UUIDFromBytes(b)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
