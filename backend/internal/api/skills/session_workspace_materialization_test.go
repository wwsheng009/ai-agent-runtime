package skills

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func TestSessionWorkspacePathMaterializationNeverOverwrites(t *testing.T) {
	if sessionNeedsWorkspacePathMaterialization(nil, "/tmp/workspace") {
		t.Fatal("nil session must not materialize")
	}
	session := &chat.Session{}
	if !sessionNeedsWorkspacePathMaterialization(session, "/tmp/workspace") {
		t.Fatal("empty context must materialize once")
	}
	session.Metadata.Context = map[string]interface{}{sessionmeta.WorkspacePath: "/bound/dir"}
	if sessionNeedsWorkspacePathMaterialization(session, "/other/dir") {
		t.Fatal("bound path must not drift to a per-turn explicit request path")
	}
	if sessionNeedsWorkspacePathMaterialization(session, "") {
		t.Fatal("empty request path must not touch the binding")
	}
}
