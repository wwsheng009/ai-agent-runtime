package siteaccount

import (
	"errors"
	"fmt"
)

// Typed errors for classification by CLI/server layers.
var (
	ErrInvalidInput      = errors.New("siteaccount: invalid input")
	ErrUnsupportedSite   = errors.New("siteaccount: unsupported site type")
	ErrHTTP              = errors.New("siteaccount: http error")
	ErrUnauthorized      = errors.New("siteaccount: unauthorized")
	ErrUnexpectedPayload = errors.New("siteaccount: unexpected payload")
	ErrMissingCredential = errors.New("siteaccount: missing credential")
)

// Error is a classified siteaccount failure.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newError(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func invalidInput(message string) error {
	return newError("INVALID_INPUT", message, ErrInvalidInput)
}

func missingCredential(message string) error {
	return newError("MISSING_CREDENTIAL", message, ErrMissingCredential)
}

func httpError(message string, cause error) error {
	return newError("HTTP_ERROR", message, errors.Join(ErrHTTP, cause))
}

func unauthorized(message string) error {
	return newError("UNAUTHORIZED", message, ErrUnauthorized)
}

func unexpectedPayload(message string, cause error) error {
	return newError("UNEXPECTED_PAYLOAD", message, errors.Join(ErrUnexpectedPayload, cause))
}

func unsupportedSite(siteType SiteType) error {
	return newError("UNSUPPORTED_SITE", fmt.Sprintf("unsupported site type %q", siteType), ErrUnsupportedSite)
}

// IsMissingCredential reports whether err is a missing-credential failure.
func IsMissingCredential(err error) bool {
	return errors.Is(err, ErrMissingCredential)
}

// IsUnauthorized reports whether err is an unauthorized failure.
func IsUnauthorized(err error) bool {
	return errors.Is(err, ErrUnauthorized)
}

// IsUnsupportedSite reports whether err is an unsupported-site failure.
func IsUnsupportedSite(err error) bool {
	return errors.Is(err, ErrUnsupportedSite)
}
