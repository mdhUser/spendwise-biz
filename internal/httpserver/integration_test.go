package httpserver_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/mdhuser/spendwise-biz/internal/authclient"
	catalogapp "github.com/mdhuser/spendwise-biz/internal/catalog/app"
	cataloghttp "github.com/mdhuser/spendwise-biz/internal/catalog/http"
	catalogmysql "github.com/mdhuser/spendwise-biz/internal/catalog/store/mysql"
	conversationapp "github.com/mdhuser/spendwise-biz/internal/conversation/app"
	conversationhttp "github.com/mdhuser/spendwise-biz/internal/conversation/http"
	conversationmysql "github.com/mdhuser/spendwise-biz/internal/conversation/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/httpserver"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	ledgerhttp "github.com/mdhuser/spendwise-biz/internal/ledger/http"
	ledgermysql "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/migrate"
	"github.com/mdhuser/spendwise-biz/internal/platform/ratelimit"
)

// TestMySQLIntegration exercises the real HTTP stack and ledger schema against
// a real MySQL database. Identity is provided by an httptest fake of
// spendwise-auth's GET /v1/auth/me, so the test proves biz never depends on the
// auth service's users/sessions tables.
//
// It only runs when BIZ_TEST_MYSQL_DSN is set and refuses databases whose name
// does not contain "test" so a mistyped DSN cannot touch a shared database.
func TestMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BIZ_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("BIZ_TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	dsnCfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse BIZ_TEST_MYSQL_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(dsnCfg.DBName), "test") {
		t.Fatalf("refusing to run integration test against database %q: name must contain \"test\"", dsnCfg.DBName)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userA := "it-user-a-" + suffix
	userB := "it-user-b-" + suffix
	tokens := map[string]string{"token-a": userA, "token-b": userB}

	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/me" {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		switch token {
		case "auth-error":
			http.Error(w, "auth unavailable", http.StatusInternalServerError)
			return
		case "auth-slow":
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":{"id":"slow-user","status":"active"}}`))
			return
		}
		userID, ok := tokens[token]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication required"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{"id": userID, "status": "active"},
		})
	}))
	t.Cleanup(fakeAuth.Close)

	t.Setenv("BIZ_MYSQL_DSN", dsn)
	t.Setenv("BIZ_MIGRATION_DSN", dsn)
	t.Setenv("BIZ_AUTH_BASE_URL", fakeAuth.URL)
	t.Setenv("BIZ_AUTH_TIMEOUT", "100ms")
	t.Setenv("BIZ_HTTP_ADDR", "127.0.0.1:0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	database, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	if err := migrate.Apply(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	for _, table := range []string{
		"accounts", "categories", "transactions", "idempotency_records", "audit_events",
		"saving_goals", "saving_goal_terms", "saving_goal_term_categories",
	} {
		var found string
		if err := database.QueryRowContext(ctx,
			`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`,
			table,
		).Scan(&found); err != nil {
			t.Fatalf("table %q not found after migration: %v", table, err)
		}
	}
	for _, check := range []struct{ table, column string }{
		{"accounts", "owner_id"},
		{"categories", "owner_id"},
		{"transactions", "owner_id"},
		{"idempotency_records", "owner_id"},
		{"audit_events", "owner_id"},
		{"audit_events", "actor_user_id"},
		{"saving_goals", "owner_id"},
		{"saving_goal_terms", "owner_id"},
		{"saving_goal_term_categories", "owner_id"},
	} {
		var dataType, columnType string
		if err := database.QueryRowContext(ctx, `
			SELECT DATA_TYPE, COLUMN_TYPE FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			check.table, check.column).Scan(&dataType, &columnType); err != nil {
			t.Fatalf("inspect %s.%s: %v", check.table, check.column, err)
		}
		if dataType != "varchar" || !strings.Contains(columnType, "191") {
			t.Fatalf("%s.%s = %s, want varchar(191)", check.table, check.column, columnType)
		}
	}
	var userFKCount int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.key_column_usage
		WHERE table_schema = DATABASE() AND referenced_table_name = 'users'
		  AND table_name IN ('accounts','categories','transactions','idempotency_records',
		                     'audit_events','saving_goals','saving_goal_terms','saving_goal_term_categories')`,
	).Scan(&userFKCount); err != nil {
		t.Fatalf("inspect users foreign keys: %v", err)
	}
	if userFKCount != 0 {
		t.Fatalf("ledger tables have %d foreign keys to auth users; want 0", userFKCount)
	}

	ledgerStore := ledgermysql.New(database)
	ledgerService := ledgerapp.New(database, ledgerStore, cfg)
	ledgerHandler := ledgerhttp.New(ledgerService, cfg.Location())
	authClient := authclient.New(cfg.AuthBaseURL, cfg.AuthTimeout)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpserver.New(
		database, logger, ledgerService, ledgerHandler, authClient, nil,
		httpserver.Config{RequestTimeout: cfg.RequestTimeout, Timezone: cfg.Location()},
	).Handler())
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = 20 * time.Second

	t.Cleanup(func() { cleanupTestData(t, database, []string{userA, userB}) })

	// 1. Auth failures map to 401/503, not to ledger errors.
	status, _, body := doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/accounts", "", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)
	requireErrorCode(t, body, "UNAUTHENTICATED")

	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/accounts", "invalid-token", "", nil)
	requireStatus(t, status, http.StatusUnauthorized, body)

	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/accounts", "auth-error", "", nil)
	requireStatus(t, status, http.StatusServiceUnavailable, body)
	requireErrorCode(t, body, "AUTH_UNAVAILABLE")

	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/accounts", "auth-slow", "", nil)
	requireStatus(t, status, http.StatusServiceUnavailable, body)
	requireErrorCode(t, body, "AUTH_UNAVAILABLE")

	tokenA := "token-a"
	tokenB := "token-b"

	// 2. Account and category.
	accountName := "集成测试账户-" + suffix
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/accounts", tokenA, "it-account-"+suffix, map[string]any{
		"name": accountName, "kind": "cash",
	})
	requireStatus(t, status, http.StatusCreated, body)
	var account idResponse
	decodeJSON(t, body, &account)
	accountID := account.ID
	if accountID == "" {
		t.Fatal("account id is empty")
	}

	categoryName := "集成测试分类-" + suffix
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/categories", tokenA, "it-category-"+suffix, map[string]any{
		"name": categoryName, "kind": "expense", "color": "#112233",
	})
	requireStatus(t, status, http.StatusCreated, body)
	var category idResponse
	decodeJSON(t, body, &category)
	categoryID := category.ID
	if categoryID == "" {
		t.Fatal("category id is empty")
	}

	// 3. Transaction + idempotent replay + mismatch.
	occurredAt := time.Now().In(cfg.Location()).Format(time.RFC3339)
	txKey := "it-tx-" + suffix
	txPayload := map[string]any{
		"type": "expense", "amountFen": 230000, "occurredAt": occurredAt,
		"categoryId": categoryID, "accountId": accountID, "note": "午饭", "tags": []string{"集成测试"},
	}
	status, headers, body := doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/transactions", tokenA, txKey, txPayload)
	requireStatus(t, status, http.StatusCreated, body)
	if headers.Get("Idempotent-Replay") != "" {
		t.Fatalf("first creation unexpectedly marked as replay: %q", headers.Get("Idempotent-Replay"))
	}
	var transaction idResponse
	decodeJSON(t, body, &transaction)
	transactionID := transaction.ID
	if transactionID == "" {
		t.Fatal("transaction id is empty")
	}

	status, headers, replayBody := doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/transactions", tokenA, txKey, txPayload)
	requireStatus(t, status, http.StatusOK, replayBody)
	if headers.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay header = %q, want true", headers.Get("Idempotent-Replay"))
	}
	var replayed idResponse
	decodeJSON(t, replayBody, &replayed)
	if replayed.ID != transactionID {
		t.Fatalf("replay id = %q, want %q", replayed.ID, transactionID)
	}

	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/transactions", tokenA, txKey, map[string]any{
		"type": "expense", "amountFen": 100, "occurredAt": occurredAt,
		"categoryId": categoryID, "accountId": accountID,
	})
	requireStatus(t, status, http.StatusConflict, body)
	requireErrorCode(t, body, "IDEMPOTENCY_MISMATCH")

	// 4. Fixed-baseline saving goal + achievements table.
	period := time.Now().In(cfg.Location()).Format("2006-01")
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/ledger/saving-goals", tokenA, "it-goal-"+suffix, map[string]any{
		"name": "每月少花 500 元", "startPeriod": period,
		"scope":           map[string]any{"kind": "all"},
		"baseline":        map[string]any{"kind": "fixed", "baselineFen": 300000},
		"targetSavingFen": 50000,
	})
	requireStatus(t, status, http.StatusCreated, body)
	var goal idResponse
	decodeJSON(t, body, &goal)
	goalID := goal.ID
	if goalID == "" {
		t.Fatal("saving goal id is empty")
	}

	// GET must return the stored version, and PATCH must accept it. This is a
	// regression test for a bug where GET incremented version in memory.
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/saving-goals/"+goalID, tokenA, "", nil)
	requireStatus(t, status, http.StatusOK, body)
	var goalDetail struct {
		Version int `json:"version"`
	}
	decodeJSON(t, body, &goalDetail)
	if goalDetail.Version != 1 {
		t.Fatalf("goal detail version = %d, want 1", goalDetail.Version)
	}
	status, _, body = doJSON(t, client, http.MethodPatch, server.URL+"/v1/ledger/saving-goals/"+goalID, tokenA, "", map[string]any{
		"name": "每月少花 500 元（已改）", "version": goalDetail.Version,
	})
	requireStatus(t, status, http.StatusOK, body)

	achievementsURL := server.URL + "/v1/ledger/saving-goals/" + goalID + "/achievements?from=" + period + "&to=" + period
	status, _, body = doJSON(t, client, http.MethodGet, achievementsURL, tokenA, "", nil)
	requireStatus(t, status, http.StatusOK, body)
	var achievements achievementResponse
	decodeJSON(t, body, &achievements)
	if len(achievements.Rows) != 1 {
		t.Fatalf("achievement rows = %d, want 1", len(achievements.Rows))
	}
	row := achievements.Rows[0]
	if row.Period != period {
		t.Errorf("achievement period = %q, want %q", row.Period, period)
	}
	if row.BaselineFen == nil || *row.BaselineFen != 300000 {
		t.Errorf("baselineFen = %v, want 300000", row.BaselineFen)
	}
	if row.SpendFen != 230000 {
		t.Errorf("spendFen = %d, want 230000", row.SpendFen)
	}
	if row.SavedFen == nil || *row.SavedFen != 70000 {
		t.Errorf("savedFen = %v, want 70000", row.SavedFen)
	}
	if row.TargetSavingFen != 50000 {
		t.Errorf("targetSavingFen = %d, want 50000", row.TargetSavingFen)
	}
	if row.Status != "in_progress" {
		t.Errorf("status = %q, want in_progress", row.Status)
	}

	// 5. Optimistic locking: successful update, then stale version conflict.
	status, _, body = doJSON(t, client, http.MethodPatch, server.URL+"/v1/ledger/accounts/"+accountID, tokenA, "", map[string]any{
		"name": accountName + "-v2", "version": 1,
	})
	requireStatus(t, status, http.StatusOK, body)
	var updatedAccount idResponse
	decodeJSON(t, body, &updatedAccount)
	if updatedAccount.Version != 2 {
		t.Fatalf("account version after update = %d, want 2", updatedAccount.Version)
	}

	status, _, body = doJSON(t, client, http.MethodPatch, server.URL+"/v1/ledger/accounts/"+accountID, tokenA, "", map[string]any{
		"name": accountName + "-v3", "version": 1,
	})
	requireStatus(t, status, http.StatusConflict, body)
	requireErrorCode(t, body, "VERSION_CONFLICT")

	// 6. Cross-user isolation: user B can neither read nor mutate A's data.
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/transactions/"+transactionID, tokenB, "", nil)
	requireStatus(t, status, http.StatusNotFound, body)
	requireErrorCode(t, body, "NOT_FOUND")

	status, _, body = doJSON(t, client, http.MethodGet, achievementsURL, tokenB, "", nil)
	requireStatus(t, status, http.StatusNotFound, body)

	status, _, body = doJSON(t, client, http.MethodPatch, server.URL+"/v1/ledger/accounts/"+accountID, tokenB, "", map[string]any{
		"name": "越权修改", "version": 2,
	})
	requireStatus(t, status, http.StatusNotFound, body)

	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/ledger/transactions", tokenB, "", nil)
	requireStatus(t, status, http.StatusOK, body)
	var list transactionListResponse
	decodeJSON(t, body, &list)
	if len(list.Items) != 0 {
		t.Fatalf("user B transaction list has %d items, want 0", len(list.Items))
	}
}

// TestCatalogConversationMySQLIntegration covers the M2 modules against a real
// MySQL schema: module-owned migrations, owner isolation, idempotent creates,
// unknown-price semantics, conversation messages and rate-limit headers.
func TestCatalogConversationMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BIZ_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("BIZ_TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	dsnCfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse BIZ_TEST_MYSQL_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(dsnCfg.DBName), "test") {
		t.Fatalf("refusing to run integration test against database %q: name must contain \"test\"", dsnCfg.DBName)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userA := "m2-user-a-" + suffix
	userB := "m2-user-b-" + suffix
	tokens := map[string]string{"token-a": userA, "token-b": userB}

	fakeAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/me" {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		userID, ok := tokens[token]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication required"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": userID, "status": "active"}})
	}))
	t.Cleanup(fakeAuth.Close)

	t.Setenv("BIZ_MYSQL_DSN", dsn)
	t.Setenv("BIZ_MIGRATION_DSN", dsn)
	t.Setenv("BIZ_AUTH_BASE_URL", fakeAuth.URL)
	t.Setenv("BIZ_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("BIZ_AUTH_TIMEOUT", "2s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	database, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrate.Apply(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	t.Cleanup(func() { cleanupM2Data(t, database, []string{userA, userB}) })

	ledgerStore := ledgermysql.New(database)
	ledgerService := ledgerapp.New(database, ledgerStore, cfg)
	ledgerHandler := ledgerhttp.New(ledgerService, cfg.Location())
	catalogHandler := cataloghttp.New(catalogapp.New(catalogmysql.New(database)))
	conversationHandler := conversationhttp.New(conversationapp.New(conversationmysql.New(database)), ratelimit.New(database))
	server := httptest.NewServer(httpserver.New(
		database, slog.New(slog.NewTextHandler(io.Discard, nil)),
		ledgerService, ledgerHandler, authclient.New(cfg.AuthBaseURL, cfg.AuthTimeout), nil,
		httpserver.Config{RequestTimeout: cfg.RequestTimeout, Timezone: cfg.Location()},
		catalogHandler, conversationHandler,
	).Handler())
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 20 * time.Second

	// Category create + idempotent replay + mismatch.
	status, _, body := doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/categories", "token-a", "m2-cat-"+suffix, map[string]any{"name": "露营"})
	requireStatus(t, status, http.StatusCreated, body)
	var category idResponse
	decodeJSON(t, body, &category)
	if category.ID == "" || category.Version != 1 {
		t.Fatalf("category = %+v", category)
	}
	status, headers, replayBody := doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/categories", "token-a", "m2-cat-"+suffix, map[string]any{"name": "露营"})
	requireStatus(t, status, http.StatusOK, replayBody)
	if headers.Get("Idempotent-Replay") != "true" {
		t.Fatalf("Idempotent-Replay = %q", headers.Get("Idempotent-Replay"))
	}
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/categories", "token-a", "m2-cat-"+suffix, map[string]any{"name": "其他"})
	requireStatus(t, status, http.StatusConflict, body)
	requireErrorCode(t, body, "IDEMPOTENCY_MISMATCH")

	// Product + SKU with an unknown shipping value.
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/products", "token-a", "m2-prod-"+suffix, map[string]any{"title": "折叠椅", "categoryId": category.ID})
	requireStatus(t, status, http.StatusCreated, body)
	var product idResponse
	decodeJSON(t, body, &product)
	if product.ID == "" || product.Version != 1 {
		t.Fatalf("product = %+v", product)
	}
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/products/"+product.ID+"/skus", "token-a", "m2-sku-"+suffix, map[string]any{
		"displayName": "黑色",
		"attributes": map[string]any{
			"weight": map[string]any{"type": "number", "value": 2.5, "unit": "kg"},
		},
		"quote": map[string]any{"priceFen": 12900, "shippingFen": nil},
	})
	requireStatus(t, status, http.StatusCreated, body)
	var sku struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
		Quotes  []struct {
			PriceFen    *int64 `json:"priceFen"`
			ShippingFen *int64 `json:"shippingFen"`
			PriceKind   string `json:"priceKind"`
		} `json:"quotes"`
	}
	decodeJSON(t, body, &sku)
	if sku.ID == "" || sku.Version != 1 || len(sku.Quotes) != 1 {
		t.Fatalf("sku = %+v", sku)
	}
	if sku.Quotes[0].PriceFen == nil || *sku.Quotes[0].PriceFen != 12900 || sku.Quotes[0].PriceKind != "exact" {
		t.Fatalf("quote = %+v", sku.Quotes[0])
	}
	if sku.Quotes[0].ShippingFen != nil {
		t.Fatalf("unknown shipping must stay nil, got %d", *sku.Quotes[0].ShippingFen)
	}

	// Library view + cross-owner isolation.
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/library", "token-a", "", nil)
	requireStatus(t, status, http.StatusOK, body)
	var library struct {
		Categories []json.RawMessage `json:"categories"`
		Products   []struct {
			ID   string `json:"id"`
			SKUs []struct {
				ID     string            `json:"id"`
				Quotes []json.RawMessage `json:"quotes"`
			} `json:"skus"`
		} `json:"products"`
	}
	decodeJSON(t, body, &library)
	if len(library.Categories) != 1 || len(library.Products) != 1 || len(library.Products[0].SKUs) != 1 || len(library.Products[0].SKUs[0].Quotes) != 1 {
		t.Fatalf("library = %+v", library)
	}
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/catalog/products/"+product.ID, "token-b", "", nil)
	requireStatus(t, status, http.StatusNotFound, body)
	status, _, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/catalog/products", "token-b", "m2-cross-"+suffix, map[string]any{"title": "越权", "categoryId": category.ID})
	requireStatus(t, status, http.StatusUnprocessableEntity, body)

	// Conversation + message + replay + owner isolation.
	status, headers, body = doJSON(t, client, http.MethodPost, server.URL+"/v1/conversations", "token-a", "m2-conv-"+suffix, map[string]any{"title": "比较"})
	requireStatus(t, status, http.StatusCreated, body)
	if headers.Get("RateLimit-Limit") == "" {
		t.Fatal("RateLimit-Limit header is missing")
	}
	var conversation idResponse
	decodeJSON(t, body, &conversation)
	if conversation.ID == "" || conversation.Version != 1 {
		t.Fatalf("conversation = %+v", conversation)
	}
	messageURL := server.URL + "/v1/conversations/" + conversation.ID + "/messages"
	status, _, body = doJSON(t, client, http.MethodPost, messageURL, "token-a", "m2-msg-"+suffix, map[string]any{"content": "帮我比较一下"})
	requireStatus(t, status, http.StatusCreated, body)
	status, headers, replayBody = doJSON(t, client, http.MethodPost, messageURL, "token-a", "m2-msg-"+suffix, map[string]any{"content": "帮我比较一下"})
	requireStatus(t, status, http.StatusOK, replayBody)
	if headers.Get("Idempotent-Replay") != "true" {
		t.Fatal("message replay header missing")
	}
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/conversations/"+conversation.ID, "token-a", "", nil)
	requireStatus(t, status, http.StatusOK, body)
	var detail struct {
		Conversation struct {
			Asks    int `json:"asks"`
			Version int `json:"version"`
		} `json:"conversation"`
		Messages []json.RawMessage `json:"messages"`
	}
	decodeJSON(t, body, &detail)
	if detail.Conversation.Asks != 1 || detail.Conversation.Version != 2 || len(detail.Messages) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	status, _, body = doJSON(t, client, http.MethodGet, server.URL+"/v1/conversations/"+conversation.ID, "token-b", "", nil)
	requireStatus(t, status, http.StatusNotFound, body)
	status, _, body = doJSON(t, client, http.MethodDelete, server.URL+"/v1/conversations/"+conversation.ID, "token-a", "", nil)
	requireStatus(t, status, http.StatusNoContent, body)
}

func cleanupM2Data(t *testing.T, database *sql.DB, ownerIDs []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, owner := range ownerIDs {
		for _, query := range []string{
			`DELETE FROM conv_conversations WHERE owner_id = ?`,
			`DELETE FROM catalog_quote_snapshots WHERE owner_id = ?`,
			`DELETE FROM catalog_user_skus WHERE owner_id = ?`,
			`DELETE FROM catalog_user_products WHERE owner_id = ?`,
			`DELETE FROM catalog_user_categories WHERE owner_id = ?`,
			`DELETE FROM catalog_user_items WHERE owner_id = ?`,
			`DELETE FROM rate_buckets WHERE bucket_key LIKE CONCAT('%', ?, '%')`,
		} {
			if _, err := database.ExecContext(ctx, query, owner); err != nil {
				t.Errorf("cleanup %q for %s: %v", query, owner, err)
			}
		}
	}
}

type idResponse struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type achievementResponse struct {
	Rows []struct {
		Period          string `json:"period"`
		BaselineFen     *int64 `json:"baselineFen"`
		SpendFen        int64  `json:"spendFen"`
		SavedFen        *int64 `json:"savedFen"`
		TargetSavingFen int64  `json:"targetSavingFen"`
		Status          string `json:"status"`
	} `json:"rows"`
}

type transactionListResponse struct {
	Items []json.RawMessage `json:"items"`
}

func doJSON(t *testing.T, client *http.Client, method, url, token, idempotencyKey string, payload any) (int, http.Header, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request payload: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, url, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, resp.Header, raw
}

func decodeJSON(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("decode JSON response %q: %v", string(body), err)
	}
}

func requireStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d; body: %s", got, want, string(body))
	}
}

func requireErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeJSON(t, body, &resp)
	if resp.Error.Code != want {
		t.Fatalf("error code = %q, want %q; body: %s", resp.Error.Code, want, string(body))
	}
}

// cleanupTestData removes only the ledger rows created by this test. Auth
// users/sessions are owned by spendwise-auth and are deliberately untouched.
func cleanupTestData(t *testing.T, database *sql.DB, ownerIDs []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	steps := []struct {
		query string
		args  int
	}{
		{`DELETE FROM saving_goal_term_categories WHERE owner_id = ?`, 1},
		{`DELETE FROM saving_goal_terms WHERE owner_id = ?`, 1},
		{`DELETE FROM saving_goals WHERE owner_id = ?`, 1},
		{`DELETE FROM transactions WHERE owner_id = ?`, 1},
		{`DELETE FROM categories WHERE owner_id = ?`, 1},
		{`DELETE FROM accounts WHERE owner_id = ?`, 1},
		{`DELETE FROM idempotency_records WHERE owner_id = ?`, 1},
		{`DELETE FROM audit_events WHERE owner_id = ? OR actor_user_id = ?`, 2},
	}
	for _, ownerID := range ownerIDs {
		for _, step := range steps {
			args := []any{ownerID}
			if step.args == 2 {
				args = append(args, ownerID)
			}
			if _, err := database.ExecContext(ctx, step.query, args...); err != nil {
				t.Errorf("cleanup %q for owner %s: %v", step.query, ownerID, err)
			}
		}
	}
}
