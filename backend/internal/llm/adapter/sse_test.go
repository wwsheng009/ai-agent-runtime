package adapter

import (
	"strings"
	"testing"
)

func TestScanSSEFrames_AssemblesMultilineDataAndResetsEvent(t *testing.T) {
	input := strings.Join([]string{
		"\uFEFFevent: first",
		"id: event-1",
		"retry: 1500",
		"data: {\"value\":",
		"data: 1}",
		"",
		": keepalive",
		"data: {\"value\":2}",
		"",
	}, "\n")

	var frames []SSEFrame
	err := scanSSEFrames(strings.NewReader(input), func(frame SSEFrame) (bool, error) {
		frames = append(frames, frame)
		return true, nil
	})
	if err != nil {
		t.Fatalf("scanSSEFrames failed: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d: %#v", len(frames), frames)
	}
	if got := frames[0]; got.Event != "first" || got.ID != "event-1" || got.Retry != 1500 || got.Data != "{\"value\":\n1}" {
		t.Fatalf("unexpected first frame: %#v", got)
	}
	if got := frames[1]; got.Event != "" || got.ID != "event-1" || got.Data != `{"value":2}` {
		t.Fatalf("expected event state to reset, got %#v", got)
	}
}

func TestScanSSEFrames_DispatchesFinalFrameAtEOF(t *testing.T) {
	var frames []SSEFrame
	err := scanSSEFrames(strings.NewReader("event: done\ndata: {}"), func(frame SSEFrame) (bool, error) {
		frames = append(frames, frame)
		return true, nil
	})
	if err != nil {
		t.Fatalf("scanSSEFrames failed: %v", err)
	}
	if len(frames) != 1 || frames[0].Event != "done" || frames[0].Data != "{}" {
		t.Fatalf("unexpected final frame: %#v", frames)
	}
}
