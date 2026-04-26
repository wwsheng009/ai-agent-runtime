package team

import (
	"errors"
	"strings"
)

// SessionExecutionError preserves structured session failure context while
// keeping the original error chain intact for errors.As / errors.Is callers.
type SessionExecutionError struct {
	Err       error
	TraceID   string
	ErrorType string
	ErrorMeta map[string]interface{}
}

func (e *SessionExecutionError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *SessionExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *SessionExecutionError) CloneMetadata() map[string]interface{} {
	if e == nil || len(e.ErrorMeta) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(e.ErrorMeta))
	for key, value := range e.ErrorMeta {
		cloned[key] = value
	}
	return cloned
}

// AsSessionExecutionError extracts a SessionExecutionError from an error chain.
func AsSessionExecutionError(err error) (*SessionExecutionError, bool) {
	if err == nil {
		return nil, false
	}
	var target *SessionExecutionError
	if errors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}

// WrapSessionExecutionError enriches err with trace / structured session error
// context surfaced by a SessionResult while preserving the original error chain.
func WrapSessionExecutionError(err error, result *SessionResult) error {
	if err == nil {
		return nil
	}
	if existing, ok := AsSessionExecutionError(err); ok && existing != nil {
		if result == nil {
			return existing
		}
		traceID := strings.TrimSpace(existing.TraceID)
		if traceID == "" {
			traceID = strings.TrimSpace(result.TraceID)
		}
		errorType := strings.TrimSpace(existing.ErrorType)
		if errorType == "" {
			errorType = strings.TrimSpace(result.ErrorType)
		}
		metadata := existing.CloneMetadata()
		if len(metadata) == 0 {
			metadata = cloneSessionExecutionMetadata(result.ErrorMetadata)
		}
		return &SessionExecutionError{
			Err:       existing.Err,
			TraceID:   traceID,
			ErrorType: errorType,
			ErrorMeta: metadata,
		}
	}
	if result == nil {
		return err
	}
	traceID := strings.TrimSpace(result.TraceID)
	errorType := strings.TrimSpace(result.ErrorType)
	metadata := cloneSessionExecutionMetadata(result.ErrorMetadata)
	if traceID == "" && errorType == "" && len(metadata) == 0 {
		return err
	}
	return &SessionExecutionError{
		Err:       err,
		TraceID:   traceID,
		ErrorType: errorType,
		ErrorMeta: metadata,
	}
}

func cloneSessionExecutionMetadata(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
