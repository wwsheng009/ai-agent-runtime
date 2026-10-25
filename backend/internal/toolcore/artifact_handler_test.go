package toolcore

import (
	"context"
	"strings"
	"testing"
)

// fakeArtifactRuntime is a surface with exactly one capability: an artifact
// store. The handler must work against it without knowing what backs it.
type fakeArtifactRuntime struct {
	sink ArtifactSink
}

func (r *fakeArtifactRuntime) Paths() PathResolver            { return nil }
func (r *fakeArtifactRuntime) Permissions() PermissionChecker { return nil }
func (r *fakeArtifactRuntime) Commands() CommandPolicy        { return nil }
func (r *fakeArtifactRuntime) Env() EnvProvider               { return nil }
func (r *fakeArtifactRuntime) Output() OutputShaper           { return nil }
func (r *fakeArtifactRuntime) Artifacts() ArtifactSink        { return r.sink }
func (r *fakeArtifactRuntime) Sessions() SessionState         { return nil }
func (r *fakeArtifactRuntime) Clock() Clock                   { return nil }
func (r *fakeArtifactRuntime) IDs() IDGenerator               { return nil }

type fakeArtifactSink struct {
	content string
	err     error
	// offset/limit record what the handler asked for.
	offset int
	limit  int
}

func (s *fakeArtifactSink) Put(context.Context, ArtifactRef, []byte, ArtifactMeta) (ArtifactRef, error) {
	return ArtifactRef{}, nil
}

func (s *fakeArtifactSink) Open(_ context.Context, _ ArtifactRef, offset, limit int) (ArtifactChunk, error) {
	s.offset, s.limit = offset, limit
	if s.err != nil {
		return ArtifactChunk{}, s.err
	}
	start := offset
	if start > len(s.content) {
		start = len(s.content)
	}
	end := start + limit
	if end > len(s.content) {
		end = len(s.content)
	}
	return ArtifactChunk{
		Data:             []byte(s.content[start:end]),
		Size:             int64(len(s.content)),
		Offset:           int64(start),
		EOF:              end >= len(s.content),
		SourceTool:       "shell",
		SourceToolCallID: "call-1",
	}, nil
}

func artifactReadCall(t *testing.T, sink ArtifactSink, args map[string]any) *Result {
	t.Helper()
	id := "art_00000000000000000000000000000000"
	h := NewArtifactReadHandler(Definition{Name: "artifact_read"})
	result, err := h.Execute(context.Background(), &Call{
		Args:    args,
		Runtime: &fakeArtifactRuntime{sink: sink},
	})
	if err != nil || result == nil {
		t.Fatalf("artifact_read failed: err=%v result=%+v", err, result)
	}
	_ = id
	return result
}

// TestArtifactPageCarriesItsOwnContinuation is the shared page contract: a
// successful result reaches the model as its body, so the header must state
// where the page sits and - when the capture continues - where to read next.
func TestArtifactPageCarriesItsOwnContinuation(t *testing.T) {
	sink := &fakeArtifactSink{content: strings.Repeat("x", 100)}
	result := artifactReadCall(t, sink, map[string]any{
		"artifact_id": "art_00000000000000000000000000000000",
		"offset":      10,
		"limit":       40,
	})
	if !result.Success {
		t.Fatalf("read failed: %s", result.Content)
	}
	if sink.offset != 10 || sink.limit != 40 {
		t.Fatalf("handler asked for offset=%d limit=%d, want 10/40", sink.offset, sink.limit)
	}
	header, window, found := strings.Cut(result.Content, "\n\n")
	if !found {
		t.Fatalf("page has no header separator: %q", result.Content)
	}
	for _, want := range []string{
		"total_bytes=100", "window=[10,50)", "eof=false", "next_offset=50",
		"tool=shell", "tool_call_id=call-1",
	} {
		if !strings.Contains(header, want) {
			t.Fatalf("header missing %q:\n%s", want, header)
		}
	}
	if len(window) != 40 {
		t.Fatalf("window holds %d bytes, want 40", len(window))
	}
	if result.Metadata["artifact_next_offset"] != 50 || result.Metadata["artifact_eof"] != false {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Metadata["artifact_has_full_bytes"] != true {
		t.Fatalf("a page of a complete record must say so: %#v", result.Metadata)
	}
}

// TestArtifactPageAtEOFStatesNoContinuation keeps the header honest at the end:
// no next_offset, eof=true, and no continuation metadata.
func TestArtifactPageAtEOFStatesNoContinuation(t *testing.T) {
	sink := &fakeArtifactSink{content: "short"}
	result := artifactReadCall(t, sink, map[string]any{
		"artifact_id": "art_00000000000000000000000000000000",
		"limit":       4096,
	})
	header, _, _ := strings.Cut(result.Content, "\n\n")
	if !strings.Contains(header, "eof=true") || strings.Contains(header, "next_offset=") {
		t.Fatalf("an EOF page must not promise a continuation:\n%s", header)
	}
	if _, ok := result.Metadata["artifact_next_offset"]; ok {
		t.Fatalf("metadata must not promise a continuation either: %#v", result.Metadata)
	}
}

// TestArtifactReadAcceptsAPastedPointerTail: a model often copies the whole
// pointer line instead of the bare id, and both surfaces must accept it the same
// way instead of failing on a string that plainly contains a valid id.
func TestArtifactReadAcceptsAPastedPointerTail(t *testing.T) {
	sink := &fakeArtifactSink{content: "body"}
	result := artifactReadCall(t, sink, map[string]any{
		"artifact_id": "Full raw output artifact_id: art_0123456789abcdef0123456789ABCDEF | size=4 | kind=structured",
	})
	if !result.Success {
		t.Fatalf("a pasted pointer tail must resolve: %s", result.Content)
	}
	if got := result.Metadata["artifact_source_id"]; got != "art_0123456789abcdef0123456789abcdef" {
		t.Fatalf("artifact_source_id = %v, want the normalized token", got)
	}
}

// TestArtifactReadKeepsTheSinkErrorClass: a store that reports WHY it refused
// (scope, expiry, corruption) must keep that class instead of collapsing into a
// generic argument error.
func TestArtifactReadKeepsTheSinkErrorClass(t *testing.T) {
	sink := &fakeArtifactSink{err: &CodedError{
		Code:       ErrArtifactExpired,
		NextAction: "re-run the command; the capture expired",
		Err:        errTestArtifactExpired,
	}}
	result := artifactReadCall(t, sink, map[string]any{
		"artifact_id": "art_00000000000000000000000000000000",
	})
	if result.Success {
		t.Fatalf("expected a failure, got %+v", result)
	}
	if result.ErrorCode != ErrArtifactExpired {
		t.Fatalf("error code = %s, want %s", result.ErrorCode, ErrArtifactExpired)
	}
	if !strings.Contains(result.Content, "re-run the command") {
		t.Fatalf("the recovery route must reach the model: %q", result.Content)
	}
}

var errTestArtifactExpired = &CodedError{Code: ErrArtifactExpired, Err: errTestArtifactExpiredCause{}}

type errTestArtifactExpiredCause struct{}

func (errTestArtifactExpiredCause) Error() string { return "artifact expired" }
