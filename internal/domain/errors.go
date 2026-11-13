package domain

import (
	"errors"
	"fmt"
)

// Code classifies an error. The HTTP layer maps codes to status codes, so the
// packages that raise errors never need to know about HTTP.
type Code string

// The error classes the service distinguishes.
const (
	CodeInvalid      Code = "invalid"
	CodeNotFound     Code = "not_found"
	CodeConflict     Code = "conflict"
	CodeUnauthorized Code = "unauthorized"
	CodeRateLimited  Code = "rate_limited"
	CodeInternal     Code = "internal"
)

// FieldError describes a problem with one input field.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// Error is a classified error whose message is safe to show to a client.
// Anything that is not an *Error is treated as internal and never shown.
type Error struct {
	Code    Code
	Message string
	Fields  []FieldError
}

// Errorf builds an *Error with a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// AddField records a problem with one input field.
func (e *Error) AddField(field, detail string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Detail: detail})
}

// CodeOf returns err's code, or CodeInternal if err is not classified.
func CodeOf(err error) Code {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return CodeInternal
}
