package commands

import "io"

// chatControlSequenceWriter routes non-editor control sequences (OSC title /
// bell) through the unified terminal session when the coordinator owns the
// physical writer, falling back to the legacy raw writer otherwise. The
// fallback is deliberate: non-unified sessions keep the exact legacy bytes.
type chatControlSequenceWriter struct {
	session *ChatSession
	raw     io.Writer
	submit  func(*chatInteractionCoordinator, string) bool
}

func (w chatControlSequenceWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if w.session != nil && w.session.Interaction != nil && w.submit != nil {
		if w.submit(w.session.Interaction, string(p)) {
			return len(p), nil
		}
	}
	if w.raw == nil {
		return len(p), nil
	}
	return w.raw.Write(p)
}
