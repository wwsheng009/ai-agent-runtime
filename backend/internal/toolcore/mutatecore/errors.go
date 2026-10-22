// Package mutatecore implements the shared write/edit/append/patch semantics.
//
// Path resolution, permission checks, session ledgers, backups and change
// notifications stay in the surface adapters. mutatecore only receives paths
// that the adapter has already resolved and authorized.
package mutatecore

import (
	"errors"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

const (
	CodeInvalidArgument = toolcore.ErrInvalidArgument
	CodeNotFound        = toolcore.ErrNotFound
	CodeUnsupported     = toolcore.ErrUnsupported
	CodePathDenied      = toolcore.ErrPathDenied
	CodePrecondition    = "WRITE_PRECONDITION_FAILED"
	CodeInternal        = toolcore.ErrInternal
)

// Error is a classified mutation failure. Adapters map Code onto their surface
// error shape without parsing prose.
type Error struct {
	Code       string
	Message    string
	NextAction string
	Actual     string
	Expected   string
	Metadata   map[string]any
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newError(code, nextAction string, err error) *Error {
	return &Error{Code: code, NextAction: nextAction, Err: err}
}

func errorf(code, nextAction, format string, args ...interface{}) *Error {
	return &Error{Code: code, NextAction: nextAction, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the stable error code carried by err, or INTERNAL_ERROR.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var mutationErr *Error
	if errors.As(err, &mutationErr) && mutationErr.Code != "" {
		return mutationErr.Code
	}
	return CodeInternal
}

// NextActionOf returns the recovery hint carried by err, if any.
func NextActionOf(err error) string {
	var mutationErr *Error
	if errors.As(err, &mutationErr) {
		return mutationErr.NextAction
	}
	return ""
}

// IsPrecondition reports whether err is a failed write precondition.
func IsPrecondition(err error) bool {
	return CodeOf(err) == CodePrecondition
}

// IsStaleContext reports whether err is a patch/edit context mismatch.
func IsStaleContext(err error) bool {
	var mutationErr *Error
	return errors.As(err, &mutationErr) && mutationErr.Code == CodeStaleContext
}

const CodeStaleContext = "STALE_CONTEXT"
