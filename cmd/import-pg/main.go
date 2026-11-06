// Command import-pg imports rebuildable demo catalog data from the old
// PostgreSQL database into MySQL.
//
// M4 scope, deliberately narrow:
//   - products -> catalog_products
//   - offers   -> catalog_offers
//
// Anonymous users, sessions, conversations, messages, snapshots and personal
// items are NOT migrated (target architecture §6.2.1 / ADR-12). IDs are mapped
// deterministically (UUIDv5) and recorded in import_id_map so reruns are
// idempotent. Use -dry-run first; the default is dry-run.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

var importNamespace = uuid.MustParse("6f9619ff-8b86-d011-b42d-00c04fc964ff")

func main() {
	_ = godotenv.Load()
	dryRun := flag.Bool("dry-run", true, "count and validate without writing")
	tables := flag.String("tables", "products,offers", "comma-separated source tables")
	batch := flag.Int("batch", 1000, "rows per transaction")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pgDSN := strings.TrimSpace(os.Getenv("IMPORT_PG_DSN"))
	if pgDSN == "" {
		logger.Error("IMPORT_PG_DSN is required")
		os.Exit(1)
	}
	selected := map[string]bool{}
	for _, table := range strings.Split(*tables, ",") {
		table = strings.TrimSpace(table)
		if table != "products" && table != "offers" {
			logger.Error("unsupported table", "table", table, "allowed", "products,offers")
			os.Exit(1)
		}
		selected[table] = true
	}

	pg, err := sql.Open("pgx", pgDSN)
	if err != nil {
		logger.Error("postgres_open", "error", err)
		os.Exit(1)
	}
	defer pg.Close()
	if err := pg.PingContext(ctx); err != nil {
		logger.Error("postgres_ping", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}
	mysqlDB, err := db.Open(cfg)
	if err != nil {
		logger.Error("mysql_open", "error", err)
		os.Exit(1)
	}
	defer mysqlDB.Close()

	importer := &importer{pg: pg, mysql: mysqlDB, batch: *batch, logger: logger}
	if *dryRun {
		if err := importer.dryRun(ctx, selected); err != nil {
			logger.Error("dry_run_failed", "error", err)
			os.Exit(1)
		}
		logger.Info("dry_run_ok", "note", "rerun with -dry-run=false to import")
		return
	}
	if err := importer.run(ctx, selected); err != nil {
		logger.Error("import_failed", "error", err)
		os.Exit(1)
	}
	if err := importer.validate(ctx, selected); err != nil {
		logger.Error("validation_failed", "error", err)
		os.Exit(1)
	}
	logger.Info("import_ok")
}

type importer struct {
	pg     *sql.DB
	mysql  *sql.DB
	batch  int
	logger *slog.Logger
}

func (i *importer) run(ctx context.Context, selected map[string]bool) error {
	if selected["products"] {
		if err := i.importProducts(ctx); err != nil {
			return err
		}
	}
	if selected["offers"] {
		if err := i.importOffers(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (i *importer) dryRun(ctx context.Context, selected map[string]bool) error {
	for _, table := range []string{"products", "offers"} {
		if !selected[table] {
			continue
		}
		var source, target int
		if err := i.pg.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&source); err != nil {
			return fmt.Errorf("count postgres %s: %w", table, err)
		}
		if err := i.mysql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+targetTable(table)).Scan(&target); err != nil {
			return fmt.Errorf("count mysql %s: %w", table, err)
		}
		i.logger.Info("dry_run_table", "source", table, "source_rows", source, "target", targetTable(table), "target_rows", target)
	}
	return nil
}

func (i *importer) validate(ctx context.Context, selected map[string]bool) error {
	for _, table := range []string{"products", "offers"} {
		if !selected[table] {
			continue
		}
		var source, mapped int
		if err := i.pg.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&source); err != nil {
			return err
		}
		if err := i.mysql.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_id_map WHERE source_table = ?`, table).Scan(&mapped); err != nil {
			return err
		}
		if source != mapped {
			return fmt.Errorf("%s: postgres=%d mapped=%d", table, source, mapped)
		}
		i.logger.Info("validated", "table", table, "rows", mapped)
	}
	return nil
}

func (i *importer) importProducts(ctx context.Context) error {
	rows, err := i.pg.QueryContext(ctx, `SELECT id, name, category, specs::text FROM products ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	return i.runBatch(ctx, rows, "products", func(tx *sql.Tx, scan func(...any) error) error {
		var sourceID, name, category string
		var specs string
		if err := scan(&sourceID, &name, &category, &specs); err != nil {
			return err
		}
		targetID := deterministicID("products", sourceID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO catalog_products (id, name, category, specs)
			VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE name = VALUES(name), category = VALUES(category),
			  specs = VALUES(specs), updated_at = UTC_TIMESTAMP(3)`,
			bin16(targetID), name, category, specs); err != nil {
			return err
		}
		return i.recordMapping(ctx, tx, "products", sourceID, "catalog_products", targetID)
	})
}

func (i *importer) importOffers(ctx context.Context) error {
	rows, err := i.pg.QueryContext(ctx, `SELECT id, product_id, payload::text, verified_at, is_demo FROM offers ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	skipped := 0
	err = i.runBatch(ctx, rows, "offers", func(tx *sql.Tx, scan func(...any) error) error {
		var sourceID, sourceProductID, payload string
		var verifiedAt time.Time
		var isDemo bool
		if err := scan(&sourceID, &sourceProductID, &payload, &verifiedAt, &isDemo); err != nil {
			return err
		}
		var rawProductID []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT target_id FROM import_id_map WHERE source_table = 'products' AND source_id = ?`,
			sourceProductID).Scan(&rawProductID); err != nil {
			if err == sql.ErrNoRows {
				skipped++
				return nil
			}
			return err
		}
		productID, err := db.UUIDFromBytes(rawProductID)
		if err != nil {
			return err
		}
		targetID := deterministicID("offers", sourceID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO catalog_offers (id, product_id, payload, verified_at, is_demo)
			VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE payload = VALUES(payload), verified_at = VALUES(verified_at),
			  is_demo = VALUES(is_demo)`,
			bin16(targetID), bin16(productID), payload, verifiedAt.UTC(), isDemo); err != nil {
			return err
		}
		return i.recordMapping(ctx, tx, "offers", sourceID, "catalog_offers", targetID)
	})
	if skipped > 0 {
		i.logger.Warn("offers_skipped_missing_product", "count", skipped)
	}
	return err
}

func (i *importer) runBatch(ctx context.Context, rows *sql.Rows, table string, handle func(*sql.Tx, func(...any) error) error) error {
	batchSize := i.batch
	if batchSize <= 0 {
		batchSize = 1000
	}
	processed := 0
	for {
		tx, err := i.mysql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		scanned := 0
		for scanned < batchSize && rows.Next() {
			if err := handle(tx, rows.Scan); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("%s batch: %w", table, err)
			}
			scanned++
			processed++
		}
		if err := rows.Err(); err != nil {
			_ = tx.Rollback()
			return err
		}
		if scanned == 0 {
			_ = tx.Rollback()
			break
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		i.logger.Info("batch_committed", "table", table, "rows", scanned, "processed", processed)
		if scanned < batchSize {
			break
		}
	}
	i.logger.Info("table_imported", "table", table, "processed", processed)
	return nil
}

func (i *importer) recordMapping(ctx context.Context, tx *sql.Tx, sourceTable, sourceID, targetTable string, targetID uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO import_id_map (source_table, source_id, target_table, target_id)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE target_table = VALUES(target_table), target_id = VALUES(target_id)`,
		sourceTable, sourceID, targetTable, bin16(targetID))
	return err
}

func deterministicID(table, sourceID string) uuid.UUID {
	return uuid.NewSHA1(importNamespace, []byte("spendwise:"+table+":"+sourceID))
}

func targetTable(source string) string {
	if source == "products" {
		return "catalog_products"
	}
	return "catalog_offers"
}

func bin16(id uuid.UUID) []byte {
	raw := make([]byte, 16)
	copy(raw, id[:])
	return raw
}
