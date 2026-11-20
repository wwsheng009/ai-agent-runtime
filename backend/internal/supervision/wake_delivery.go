package supervision

import (
	"errors"
	"strings"
)

// WakeReasonResultsConsumptionRetryPrefix is the control-plane-only wake
// family used to repair a results-consumption marker after prompt acceptance.
// A per-turn suffix prevents repairs for different turns from coalescing.
const WakeReasonResultsConsumptionRetryPrefix = "turn_results_consumption_retry"

// ResultsConsumptionRetryWakeReason builds a durable marker-only wake reason.
func ResultsConsumptionRetryWakeReason(turnID string) string {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return WakeReasonResultsConsumptionRetryPrefix
	}
	return WakeReasonResultsConsumptionRetryPrefix + ":" + turnID
}

// IsResultsConsumptionRetryWake reports whether reason names the marker-only
// repair path.
func IsResultsConsumptionRetryWake(reason string) bool {
	reason = strings.TrimSpace(reason)
	return strings.EqualFold(reason, WakeReasonResultsConsumptionRetryPrefix) ||
		strings.HasPrefix(strings.ToLower(reason), strings.ToLower(WakeReasonResultsConsumptionRetryPrefix)+":")
}

// AcceptedDeliveryError reports a failure that happened after the host had
// durably accepted the wake prompt.  The acceptance boundary is deliberately
// separate from the post-acceptance work: a caller must not submit the same
// prompt again merely because a marker or the model run failed afterwards.
// RetryMarker requests a host-provided marker-only repair; it never authorizes
// another model prompt submission.
type AcceptedDeliveryError struct {
	Err         error
	RetryMarker bool
}

func (e *AcceptedDeliveryError) Error() string {
	if e == nil || e.Err == nil {
		return "supervision wake delivery accepted with post-acceptance failure"
	}
	return e.Err.Error()
}

func (e *AcceptedDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewAcceptedDeliveryError wraps a post-acceptance failure.  A nil error is
// kept nil so callers can use it at the receipt boundary without inventing a
// failure.
func NewAcceptedDeliveryError(err error, retryMarker bool) error {
	if err == nil {
		return nil
	}
	return &AcceptedDeliveryError{Err: err, RetryMarker: retryMarker}
}

// AcceptedDelivery extracts the post-acceptance classification from err.
func AcceptedDelivery(err error) (*AcceptedDeliveryError, bool) {
	if err == nil {
		return nil, false
	}
	var accepted *AcceptedDeliveryError
	if !errors.As(err, &accepted) || accepted == nil {
		return nil, false
	}
	return accepted, true
}

func joinDeliveryErrors(primary, secondary error) error {
	return errors.Join(primary, secondary)
}
