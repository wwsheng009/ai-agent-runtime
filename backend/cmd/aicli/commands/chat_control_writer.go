package commands

import "io"

// chatControlSequenceWriter routes non-editor control sequences (OSC title /
// bell) through the unified terminal session when the coordinator owns the
// physical writer, falling back to the legacy raw writer otherwise. The
// fallback is deliberate: non-unified sessions keep the exact legacy bytes.
//
// Unified sessions fail closed (gap G2/A1-3): if the session submit fails, the
// sequence is dropped rather than written raw — a raw os.Std* write would
// interleave with the session writer's frames.
type chatControlSequenceWriter struct {
	session *ChatSession
	raw     io.Writer
	submit  func(*chatInteractionCoordinator, string) bool
}

func (w chatControlSequenceWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if w.session != nil && w.session.Interaction != nil {
		unified := w.session.Interaction.UnifiedRendererActive()
		if w.submit != nil && w.submit(w.session.Interaction, string(p)) {
			return len(p), nil
		}
		if unified {
			// Fail-closed: the unified renderer owns the physical writer; a
			// raw fallback would bypass the session and corrupt frame order.
			return len(p), nil
		}
	}
	if w.raw == nil {
		return len(p), nil
	}
	return w.raw.Write(p)
}
