package apperr

import "fmt"

type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func Validation(message string) *Error {
	return New(422, "VALIDATION_FAILED", message)
}

func NotFound(message string) *Error {
	return New(404, "NOT_FOUND", message)
}

func Conflict(code, message string) *Error {
	return New(409, code, message)
}

func Unauthenticated(message string) *Error {
	return New(401, "UNAUTHENTICATED", message)
}

func Forbidden(message string) *Error {
	return New(403, "FORBIDDEN", message)
}
