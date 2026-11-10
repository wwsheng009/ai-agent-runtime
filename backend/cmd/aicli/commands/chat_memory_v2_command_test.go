package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestStructuredMemoryV2Commands(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "runtime.yaml")
	if err := os.WriteFile(configPath, []byte(`
memory:
  mode: on
  injection:
    mode: summary
`), 0o644); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
	session := &ChatSession{
		WorkspaceAnchor:   root,
		RuntimeConfigPath: configPath,
		RuntimeSession:    &runtimechat.Session{ID: "memory-v2-command-test"},
	}
	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	note, err := store.Append(memorystore.AppendNoteOptions{
		Text: "original durable memory", Kind: "decision", SessionID: "memory-v2-command-test",
	})
	if err != nil {
		t.Fatalf("append seed: %v", err)
	}
	_ = store.Close()

	for _, command := range []string{
		"/memory show " + note.ID,
		"/memory why " + note.ID,
		"/memory why-not memory",
		"/memory correct " + note.ID + " corrected durable memory",
		"/memory stale",
		"/memory jobs",
		"/memory consolidate",
		"/memory export " + filepath.Join(root, "memory-export.jsonl"),
		"/memory forget " + note.ID + " --yes",
		"/memory reset --yes",
	} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s structured match=(%t, %v)", command, handled, err)
		}
		if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) == "" {
			t.Fatalf("%s rendered an empty document", command)
		}
	}
}
