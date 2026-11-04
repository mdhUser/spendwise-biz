package httpx

import (
	"errors"
	"net/http"

	"github.com/go-sql-driver/mysql"
)

// mysqlAPIError maps MySQL server errors to stable API errors so handlers do
// not have to duplicate driver-specific error handling. Errors that already
// map to apperr/APIError are handled by WriteError before this function runs.
func mysqlAPIError(err error) (*APIError, bool) {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return nil, false
	}
	switch mysqlErr.Number {
	case 1062, // ER_DUP_ENTRY
		1451, // ER_ROW_IS_REFERENCED_2
		1452: // ER_NO_REFERENCED_ROW_2
		return New(http.StatusConflict, "CONFLICT", "数据冲突，请刷新后重试"), true
	case 3819, // ER_CHECK_CONSTRAINT_VIOLATED
		1406, // ER_DATA_TOO_LONG
		1366, // ER_TRUNCATED_WRONG_VALUE_FOR_FIELD
		1264, // ER_WARN_DATA_OUT_OF_RANGE
		1292: // ER_TRUNCATED_WRONG_VALUE
		return New(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "提交的数据不合法"), true
	case 1213, // ER_LOCK_DEADLOCK
		1205, // ER_LOCK_WAIT_TIMEOUT
		1040, // ER_CON_COUNT_ERROR
		1146: // ER_NO_SUCH_TABLE
		return New(http.StatusServiceUnavailable, "UNAVAILABLE", "数据库暂时不可用，请稍后再试"), true
	default:
		return New(http.StatusInternalServerError, "INTERNAL", "服务暂时不可用，请稍后再试"), true
	}
}
