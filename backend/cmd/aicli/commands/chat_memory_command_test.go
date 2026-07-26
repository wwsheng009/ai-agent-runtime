package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestHandleMemoryCommand_AddListSearch(t *testing.T) {
	root := t.TempDir()
	session := &ChatSession{
		ProfileRoot: root,
		RuntimeSession: &runtimechat.Session{
			ID: "memory-cmd-test",
		},
	}

	addOut := captureStdout(t, func() {
		if handleMemoryCommand(session, "/memory add Prefer worktree isolation for parallel agents") {
			t.Fatalf("add should not exit chat")
		}
	})
	if !strings.Contains(addOut, "已写入项目记忆") {
		t.Fatalf("expected add confirmation, got %q", addOut)
	}

	expectedDir := filepath.Join(root, filepath.FromSlash(memorystore.DefaultDirName))
	notesPath := filepath.Join(expectedDir, memorystore.DefaultNotesFile)
	if _, err := os.Stat(notesPath); err != nil {
		t.Fatalf("expected notes file at %s: %v", notesPath, err)
	}

	listOut := captureStdout(t, func() {
		if handleMemoryCommand(session, "/memory list 5") {
			t.Fatalf("list should not exit chat")
		}
	})
	if !strings.Contains(listOut, "worktree isolation") {
		t.Fatalf("expected listed note, got %q", listOut)
	}

	searchOut := captureStdout(t, func() {
		if handleMemoryCommand(session, "/memory search worktree") {
			t.Fatalf("search should not exit chat")
		}
	})
	if !strings.Contains(searchOut, "worktree") {
		t.Fatalf("expected search hit, got %q", searchOut)
	}

	statusOut := captureStdout(t, func() {
		if handleMemoryCommand(session, "/memory status") {
			t.Fatalf("status should not exit chat")
		}
	})
	if !strings.Contains(statusOut, "项目记忆 root=") {
		t.Fatalf("expected status root line, got %q", statusOut)
	}
	if !strings.Contains(statusOut, "total=1") {
		t.Fatalf("expected total=1 in status, got %q", statusOut)
	}
}
