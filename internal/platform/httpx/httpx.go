package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	maxBodyBytes            = 1 << 20
)

type APIError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func WithDetails(status int, code, message string, details any) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Details: details}
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return New(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请求必须使用 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return New(http.StatusBadRequest, "BAD_REQUEST", "请求不能为空")
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体超过限制")
		}
		return New(http.StatusBadRequest, "BAD_REQUEST", "JSON 格式错误或包含未知字段")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return New(http.StatusBadRequest, "BAD_REQUEST", "请求只能包含一个 JSON 对象")
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if value == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := &APIError{Status: http.StatusInternalServerError, Code: "INTERNAL", Message: "服务暂时不可用，请稍后再试"}
	var known *APIError
	var appKnown *apperr.Error
	switch {
	case errors.As(err, &known):
		apiErr = known
	case errors.As(err, &appKnown):
		apiErr = &APIError{Status: appKnown.Status, Code: appKnown.Code, Message: appKnown.Message, Details: appKnown.Details}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		apiErr = New(http.StatusServiceUnavailable, "UNAVAILABLE", "请求超时，请稍后再试")
	}
	body := map[string]any{
		"error": map[string]any{
			"code":      apiErr.Code,
			"message":   apiErr.Message,
			"requestId": RequestID(r.Context()),
		},
	}
	if apiErr.Details != nil {
		body["error"].(map[string]any)["details"] = apiErr.Details
	}
	WriteJSON(w, apiErr.Status, body)
}
