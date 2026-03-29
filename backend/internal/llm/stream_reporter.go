package llm

import "context"

type streamReporterContextKey struct{}

// StreamReporter receives incremental model stream chunks from Call paths that parse SSE responses.
type StreamReporter func(StreamChunk)

// WithStreamReporter attaches a stream reporter to the context.
func WithStreamReporter(ctx context.Context, reporter StreamReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, streamReporterContextKey{}, reporter)
}

func reportStreamChunk(ctx context.Context, chunk StreamChunk) {
	if ctx == nil {
		return
	}
	reporter, _ := ctx.Value(streamReporterContextKey{}).(StreamReporter)
	if reporter == nil {
		return
	}
	reporter(chunk)
}
