package toolcore

import "errors"

// CodedError carries a stable tool error code and recovery hint across an
// adapter boundary (for example an ArtifactSink implementation calling into a
// surface-local store). Handlers use CodeOf/NextActionOf to preserve the code
// instead of collapsing every failure into INVALID_ARGUMENT.
type CodedError struct {
	Code       string
	NextAction string
	Err        error
}

func (e *CodedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *CodedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CodeOf returns the stable code carried by err, or an empty string.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return NormalizeErrorCode(coded.Code)
	}
	return ""
}

// NextActionOf returns the recovery hint carried by err, or an empty string.
func NextActionOf(err error) string {
	if err == nil {
		return ""
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.NextAction
	}
	return ""
}
