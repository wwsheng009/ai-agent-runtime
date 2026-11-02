package remotetools

import "context"

// callWorkdirKey carries a per-call working directory through the context.
//
// Why a context value rather than a parameter threaded through every handler:
// path resolution for every remote tool funnels through
// remoteToolBase.resolve, which already receives a context. Putting the
// per-call directory in the context lets a single wrapper (prefixedTool, which
// wraps every registered tool) add the capability once, instead of touching
// fourteen handlers and risking an inconsistent default.
//
// The value is scoped to exactly one tool call: prefixedTool.Execute derives a
// child context and never stores it anywhere. Nothing here is sticky, so the
// documented contract still holds — a workdir applies to the call that passed
// it and to nothing else.
type callWorkdirKey struct{}

// WithCallWorkdir returns a child context carrying the requested working
// directory for this single call. An empty value is a no-op so callers can pass
// the extracted parameter unconditionally.
func WithCallWorkdir(ctx context.Context, workdir string) context.Context {
	if workdir == "" {
		return ctx
	}
	return context.WithValue(ctx, callWorkdirKey{}, workdir)
}

// CallWorkdir extracts the per-call working directory, or "" when unset.
func CallWorkdir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(callWorkdirKey{}).(string); ok {
		return v
	}
	return ""
}

// pathParams are the schema keys whose values are filesystem paths. A tool that
// declares any of them can meaningfully accept a workdir base, so the schema
// wrapper injects the parameter for exactly those tools. Tools that already
// declare "workdir" themselves (shell, job_start) are excluded by the caller.
var pathParams = []string{"path", "paths", "files", "file_path", "source", "destination", "input", "patch"}

// declaresPathParam reports whether the schema's properties include any
// filesystem path parameter.
func declaresPathParam(schema map[string]interface{}) bool {
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, key := range pathParams {
		if _, found := props[key]; found {
			return true
		}
	}
	return false
}

// workdirProperty is the shared schema fragment injected into path-taking tools.
// The description is deliberately explicit about the three things models get
// wrong: the anchor (session workspace root), that it is not remembered across
// calls, and that the directory must exist.
func workdirProperty() map[string]interface{} {
	return map[string]interface{}{
		"type": "string",
		"description": "Optional directory base for THIS call. When set, relative path parameters of the " +
			"same call are resolved against it; absolute path parameters are unaffected. Relative values are " +
			"themselves resolved against the session workspace root. Omit to use the session workspace root. " +
			"The directory must already exist and must be inside the allowed roots; a file is rejected. " +
			"workdir only moves the base for relative paths; it does NOT narrow what is reachable, " +
			"because a relative path may still climb out with '..' as long as it stays inside the allowed " +
			"roots. Use it for convenience, not as a sandbox restriction. " +
			"The choice is not remembered by later calls.",
	}
}

// withWorkdirProperty returns a copy of schema whose properties include workdir.
//
// The copy matters: tool schemas are built once and shared, so mutating the map
// returned by inner.Parameters() would leak the parameter into every surface and
// could double-apply on a second wrap.
func withWorkdirProperty(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return schema
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return schema
	}
	if _, exists := props["workdir"]; exists {
		return schema
	}
	copiedProps := make(map[string]interface{}, len(props)+1)
	for k, v := range props {
		copiedProps[k] = v
	}
	copiedProps["workdir"] = workdirProperty()
	copied := make(map[string]interface{}, len(schema))
	for k, v := range schema {
		copied[k] = v
	}
	copied["properties"] = copiedProps
	return copied
}
