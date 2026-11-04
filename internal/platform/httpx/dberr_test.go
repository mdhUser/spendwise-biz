package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
)

type apiErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
	} `json:"error"`
}

func writeErrorResponse(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/transactions", nil)
	req = req.WithContext(WithRequestID(req.Context(), "req-test-1"))
	rec := httptest.NewRecorder()
	WriteError(rec, req, err)
	return rec
}

func decodeAPIError(t *testing.T, rec *httptest.ResponseRecorder) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if body.Error.RequestID != "req-test-1" {
		t.Fatalf("requestId = %q, want req-test-1", body.Error.RequestID)
	}
	return body
}

func TestWriteErrorMapsMySQLErrors(t *testing.T) {
	cases := []struct {
		number uint16
		status int
		code   string
	}{
		{1062, http.StatusConflict, "CONFLICT"},
		{1451, http.StatusConflict, "CONFLICT"},
		{1452, http.StatusConflict, "CONFLICT"},
		{3819, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{1406, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{1366, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{1264, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{1292, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{1213, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{1205, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{1040, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{1146, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{9999, http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("mysql_%d", tc.number), func(t *testing.T) {
			err := fmt.Errorf("query failed: %w", &mysql.MySQLError{
				Number: tc.number, Message: "sensitive database detail",
			})
			rec := writeErrorResponse(t, err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			body := decodeAPIError(t, rec)
			if body.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", body.Error.Code, tc.code)
			}
			if strings.Contains(body.Error.Message, "sensitive") {
				t.Fatalf("response leaked mysql message: %q", body.Error.Message)
			}
		})
	}
}

func TestWriteErrorKeepsAppErrorMappingBeforeMySQL(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"api_error", New(http.StatusTeapot, "TEAPOT", "短消息"), http.StatusTeapot, "TEAPOT"},
		{"app_validation", apperr.Validation("金额不合法"), http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"app_version_conflict", apperr.Conflict("VERSION_CONFLICT", "流水已被修改"), http.StatusConflict, "VERSION_CONFLICT"},
		{"plain_error", errors.New("boom"), http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := writeErrorResponse(t, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			body := decodeAPIError(t, rec)
			if body.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", body.Error.Code, tc.code)
			}
		})
	}
}
