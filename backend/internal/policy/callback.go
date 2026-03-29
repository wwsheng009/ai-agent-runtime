package policy

import "context"

// CanUseToolCallback allows external callers to influence tool permission decisions.
type CanUseToolCallback func(ctx context.Context, req EvalRequest) (Decision, string, error)
