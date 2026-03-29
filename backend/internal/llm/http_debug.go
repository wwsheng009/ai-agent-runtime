package llm

import "context"

// HTTPDebugEvent captures a low-level HTTP request/response snapshot emitted by runtime LLM providers.
type HTTPDebugEvent struct {
	Source              string `json:"source,omitempty"`
	Phase               string `json:"phase,omitempty"`
	Provider            string `json:"provider,omitempty"`
	Protocol            string `json:"protocol,omitempty"`
	Model               string `json:"model,omitempty"`
	Method              string `json:"method,omitempty"`
	URL                 string `json:"url,omitempty"`
	RequestBody         string `json:"request_body,omitempty"`
	RequestBodyBytes    int    `json:"request_body_bytes,omitempty"`
	ResponseStatusCode  int    `json:"response_status_code,omitempty"`
	ResponseBodyPreview string `json:"response_body_preview,omitempty"`
	ResponseBodyBytes   int    `json:"response_body_bytes,omitempty"`
	Error               string `json:"error,omitempty"`
}

// HTTPDebugReporter consumes runtime HTTP debug events.
type HTTPDebugReporter func(HTTPDebugEvent)

type httpDebugReporterContextKey struct{}

// WithHTTPDebugReporter attaches a runtime HTTP debug reporter to the context.
func WithHTTPDebugReporter(ctx context.Context, reporter HTTPDebugReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpDebugReporterContextKey{}, reporter)
}

func reportHTTPDebug(ctx context.Context, event HTTPDebugEvent) {
	if ctx == nil {
		return
	}
	reporter, _ := ctx.Value(httpDebugReporterContextKey{}).(HTTPDebugReporter)
	if reporter == nil {
		return
	}
	reporter(event)
}

func truncateHTTPDebugText(text string, maxBytes int) string {
	if text == "" || maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	return text[:maxBytes]
}
