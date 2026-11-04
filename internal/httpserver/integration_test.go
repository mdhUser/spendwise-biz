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
	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/httpserver"
	identityapp "github.com/mdhuser/spendwise-biz/internal/identity/app"
	identityhttp "github.com/mdhuser/spendwise-biz/internal/identity/http"
	identitymysql "github.com/mdhuser/spendwise-biz/internal/identity/store/mysql"
	ledgerapp "github.com/mdhuser/spendwise-biz/internal/ledger/app"
	ledgerhttp "github.com/mdhuser/spendwise-biz/internal/ledger/http"
	ledgermysql "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/config"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/migrate"
)

// TestMySQLIntegration exercises the real HTTP stack against a real MySQL
// database, starting from a fresh migration run. It only runs when
// BIZ_TEST_MYSQL_DSN is set, and it refuses databases whose name does not
// contain "test" so a mistyped DSN cannot touch a shared database.
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

	t.Setenv("BIZ_MYSQL_DSN", dsn)
	t.Setenv("BIZ_MIGRATION_DSN", dsn)
	t.Setenv("BIZ_INVITE_PEPPER", strings.Repeat("integration-test-pepper-", 3))
	t.Setenv("BIZ_REGISTRATION_MODE", "invite")
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

	// 1. Migrations: identity then ledger.
	if err := migrate.Apply(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	for _, table := range []string{
		"users", "sessions", "invites",
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

	identityStore := identitymysql.New(database)
	identityService := identityapp.New(database, identityStore, cfg)
	ledgerStore := ledgermysql.New(database)
	ledgerService := ledgerapp.New(database, ledgerStore, cfg)

	identityService.SetDefaults(func(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error {
		return ledgerService.EnsureDefaultsTx(ctx, tx, userID)
	})
	identityHandler := identityhttp.New(identityService)
	identityHandler.SetPurge(func(ctx context.Context, tx *sql.Tx, userID uuid.UUID) error {
		return ledgerService.PurgeUser(ctx, tx, userID)
	})
	ledgerHandler := ledgerhttp.New(ledgerService, cfg.Location())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpserver.New(
		database, logger, identityService, ledgerService, identityHandler, ledgerHandler,
		httpserver.Config{RequestTimeout: cfg.RequestTimeout, Timezone: cfg.Location()},
	).Handler())
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = 20 * time.Second

	// Cleanup state is populated as users/invites are created. Cleanup runs
	// before the database is closed (LIFO) and only touches the IDs this test
	// created, in foreign-key dependency order.
	var createdUsers []uuid.UUID
	var createdInvites []uuid.UUID
	t.Cleanup(func() { cleanupTestData(t, database, createdUsers, createdInvites) })

	newInvite := func(t *testing.T) string {
		t.Helper()
		code, invite, err := identityService.CreateInvite(ctx, 5, time.Hour)
		if err != nil {
			t.Fatalf("create invite: %v", err)
		}
		createdInvites = append(createdInvites, invite.ID)
		return code
	}

	register := func(t *testing.T, email, password, inviteCode string) string {
		t.Helper()
		status, _, body := doJSON(t, client, http.MethodPost, server.URL+"/v1/auth/register", "", "", map[string]any{
			"email": email, "password": password, "displayName": "集成测试", "inviteCode": inviteCode,
		})
		requireStatus(t, status, http.StatusCreated, body)
		var resp authResponse
		decodeJSON(t, body, &resp)
		userID, err := uuid.Parse(resp.User.ID)
		if err != nil {
			t.Fatalf("parse registered user id %q: %v", resp.User.ID, err)
		}
		if resp.Token == "" {
			t.Fatal("register returned an empty token")
		}
		createdUsers = append(createdUsers, userID)
		return resp.Token
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	password := "correct horse battery staple"
	emailA := "it-a-" + suffix + "@example.com"

	// 2. Invite -> register -> login.
	register(t, emailA, password, newInvite(t))
	status, _, body := doJSON(t, client, http.MethodPost, server.URL+"/v1/auth/login", "", "", map[string]any{
		"email": emailA, "password": password,
	})
	requireStatus(t, status, http.StatusOK, body)
	var login authResponse
	decodeJSON(t, body, &login)
	if login.Token == "" {
		t.Fatal("login returned an empty token")
	}
	tokenA := login.Token

	// 3. Account and category.
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

	// 4. Transaction + idempotent replay + mismatch.
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

	// 5. Fixed-baseline saving goal + achievements table.
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

	// 6. Optimistic locking: successful update, then stale version conflict.
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

	// 7. Cross-user isolation: user B can neither read nor mutate A's data.
	emailB := "it-b-" + suffix + "@example.com"
	tokenB := register(t, emailB, password, newInvite(t))

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

type authResponse struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Token string `json:"token"`
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

// cleanupTestData removes only the users and invites created by this test, in
// foreign-key dependency order. Users are deleted last because most ledger
// tables reference them with ON DELETE RESTRICT.
func cleanupTestData(t *testing.T, database *sql.DB, userIDs, inviteIDs []uuid.UUID) {
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
		{`DELETE FROM sessions WHERE user_id = ?`, 1},
		{`DELETE FROM idempotency_records WHERE owner_id = ?`, 1},
		{`DELETE FROM audit_events WHERE owner_id = ? OR actor_user_id = ?`, 2},
		{`DELETE FROM users WHERE id = ?`, 1},
	}
	for _, userID := range userIDs {
		id := db.UUIDBytes(userID)
		for _, step := range steps {
			args := []any{id}
			if step.args == 2 {
				args = append(args, id)
			}
			if _, err := database.ExecContext(ctx, step.query, args...); err != nil {
				t.Errorf("cleanup %q for user %s: %v", step.query, userID, err)
			}
		}
	}
	for _, inviteID := range inviteIDs {
		if _, err := database.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, db.UUIDBytes(inviteID)); err != nil {
			t.Errorf("cleanup invite %s: %v", inviteID, err)
		}
	}
}
