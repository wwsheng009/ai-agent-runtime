package knowledge

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNodeTypeClosedSet(t *testing.T) {
	valid := []NodeType{NodeTypeFile, NodeTypeSymbol, NodeTypeQuery, NodeTypeAnswer}
	for _, nodeType := range valid {
		if !nodeType.Valid() {
			t.Errorf("NodeType(%q).Valid() = false, want true", nodeType)
		}
		parsed, err := ParseNodeType(string(nodeType))
		if err != nil || parsed != nodeType {
			t.Errorf("ParseNodeType(%q) = %q, %v; want %q", nodeType, parsed, err, nodeType)
		}
	}
	if NodeType("").Valid() {
		t.Error(`NodeType("").Valid() = true, want false`)
	}
	if NodeType("HINT").Valid() {
		t.Error(`NodeType("HINT").Valid() = true, want false（Valid 不做大小写归一）`)
	}
	parsed, err := ParseNodeType("  FILE ")
	if err != nil || parsed != NodeTypeFile {
		t.Errorf("ParseNodeType(trim/lower) = %q, %v; want %q", parsed, err, NodeTypeFile)
	}
	if _, err := ParseNodeType("hint"); err == nil {
		t.Error("ParseNodeType(hint) succeeded, want error")
	}
}

func TestEdgeTypeClosedSet(t *testing.T) {
	valid := []EdgeType{EdgeTypeCalls, EdgeTypeReferences, EdgeTypeContains, EdgeTypeDerivedFrom}
	for _, edgeType := range valid {
		if !edgeType.Valid() {
			t.Errorf("EdgeType(%q).Valid() = false, want true", edgeType)
		}
		parsed, err := ParseEdgeType(string(edgeType))
		if err != nil || parsed != edgeType {
			t.Errorf("ParseEdgeType(%q) = %q, %v; want %q", edgeType, parsed, err, edgeType)
		}
	}
	if EdgeType("imports").Valid() {
		t.Error(`EdgeType("imports").Valid() = true, want false`)
	}
	if _, err := ParseEdgeType("imports"); err == nil {
		t.Error("ParseEdgeType(imports) succeeded, want error")
	}
}

func TestExplorationIDsStable(t *testing.T) {
	sessionID := ExplorationSessionID("w1", "s1", "t1")
	if !strings.HasPrefix(sessionID, "es_") {
		t.Errorf("session id = %q, want es_ prefix", sessionID)
	}
	if sessionID != ExplorationSessionID("w1", "s1", "t1") {
		t.Error("ExplorationSessionID is not deterministic for identical input")
	}
	if sessionID == ExplorationSessionID("w1", "s1", "t2") {
		t.Error("ExplorationSessionID must differ across tasks")
	}
	if sessionID == ExplorationSessionID("w1", "s2", "t1") {
		t.Error("ExplorationSessionID must differ across sessions")
	}

	nodeID := ExplorationNodeID("es_1", "pkg/a.go")
	if !strings.HasPrefix(nodeID, "en_") {
		t.Errorf("node id = %q, want en_ prefix", nodeID)
	}
	if nodeID != ExplorationNodeID("es_1", "pkg/a.go") {
		t.Error("ExplorationNodeID is not deterministic for identical input")
	}
	if nodeID == ExplorationNodeID("es_2", "pkg/a.go") {
		t.Error("ExplorationNodeID must differ across explorations")
	}
	if nodeID == ExplorationNodeID("es_1", "pkg/b.go") {
		t.Error("ExplorationNodeID must differ across targets")
	}

	edgeID := ExplorationEdgeID("es_1", "en_1", "en_2", EdgeTypeCalls)
	if !strings.HasPrefix(edgeID, "ee_") {
		t.Errorf("edge id = %q, want ee_ prefix", edgeID)
	}
	if edgeID != ExplorationEdgeID("es_1", "en_1", "en_2", EdgeTypeCalls) {
		t.Error("ExplorationEdgeID is not deterministic for identical input")
	}
	if edgeID == ExplorationEdgeID("es_1", "en_1", "en_2", EdgeTypeReferences) {
		t.Error("ExplorationEdgeID must differ across edge types")
	}
}

func TestExplorationScopeValidation(t *testing.T) {
	validSession := ExplorationSession{WorkspaceID: "w", SessionID: "s", TaskID: "t"}
	if err := validSession.Validate(); err != nil {
		t.Errorf("valid session rejected: %v", err)
	}
	if err := (ExplorationSession{SessionID: "s"}).Validate(); err == nil {
		t.Error("session without workspace_id accepted")
	}
	if err := (ExplorationSession{WorkspaceID: "w"}).Validate(); err == nil {
		t.Error("session without session_id accepted")
	}
	if err := (ExplorationSession{WorkspaceID: "w", SessionID: "s"}).Validate(); err != nil {
		t.Errorf("session without task_id must be valid: %v", err)
	}

	validNode := ExplorationNode{
		ExplorationID:    "es_1",
		NodeType:         NodeTypeFile,
		Target:           "pkg/a.go",
		KnowledgeVersion: "wv1_x",
	}
	if err := validNode.Validate(); err != nil {
		t.Errorf("valid node rejected: %v", err)
	}
	if err := (ExplorationNode{NodeType: NodeTypeFile, Target: "a", KnowledgeVersion: "v"}).Validate(); err == nil {
		t.Error("node without exploration_id accepted")
	}
	if err := (ExplorationNode{ExplorationID: "es", Target: "a", KnowledgeVersion: "v"}).Validate(); err == nil {
		t.Error("node without node_type accepted")
	}
	if err := (ExplorationNode{ExplorationID: "es", NodeType: NodeTypeFile, KnowledgeVersion: "v"}).Validate(); err == nil {
		t.Error("node without target accepted")
	}
	if err := (ExplorationNode{ExplorationID: "es", NodeType: NodeTypeFile, Target: "a"}).Validate(); err == nil {
		t.Error("node without knowledge_version accepted")
	}
	outOfRange := validNode
	outOfRange.Confidence = 1.5
	if err := outOfRange.Validate(); err == nil {
		t.Error("node with confidence > 1 accepted")
	}

	validEdge := ExplorationEdge{ExplorationID: "es", FromNodeID: "en_1", ToNodeID: "en_2", EdgeType: EdgeTypeCalls}
	if err := validEdge.Validate(); err != nil {
		t.Errorf("valid edge rejected: %v", err)
	}
	if err := (ExplorationEdge{FromNodeID: "a", ToNodeID: "b", EdgeType: EdgeTypeCalls}).Validate(); err == nil {
		t.Error("edge without exploration_id accepted")
	}
	if err := (ExplorationEdge{ExplorationID: "es", ToNodeID: "b", EdgeType: EdgeTypeCalls}).Validate(); err == nil {
		t.Error("edge without from_node_id accepted")
	}
	if err := (ExplorationEdge{ExplorationID: "es", FromNodeID: "a", EdgeType: EdgeTypeCalls}).Validate(); err == nil {
		t.Error("edge without to_node_id accepted")
	}
	if err := (ExplorationEdge{ExplorationID: "es", FromNodeID: "a", ToNodeID: "b"}).Validate(); err == nil {
		t.Error("edge without edge_type accepted")
	}
	if err := (ExplorationEdge{ExplorationID: "es", FromNodeID: "a", ToNodeID: "b", EdgeType: EdgeTypeCalls, Weight: -1}).Validate(); err == nil {
		t.Error("edge with negative weight accepted")
	}
}

// TestWorkspaceVersionHashSensitivity 钉住版本函数的敏感性（W1 测试清单）：
// 文件哈希 / adapter / schema / workspace / 文件集合任一变化都必须改变版本；
// 同输入（含文件顺序变化）必须得到同一版本。
func TestWorkspaceVersionHashSensitivity(t *testing.T) {
	files := []FileRecord{
		{Path: "pkg/b.go", ContentHash: "hash-b"},
		{Path: `pkg\a.go`, ContentHash: "hash-a"},
	}
	base := workspaceVersionHash("w1", "builtin/3", 2, files)
	if !strings.HasPrefix(base, "wv1_") {
		t.Errorf("workspace version = %q, want wv1_ prefix", base)
	}
	if got := workspaceVersionHash("w1", "builtin/3", 2, files); got != base {
		t.Errorf("same input produced different version: %q vs %q", got, base)
	}
	// 输入顺序与 path 分隔符不得影响结果（Windows 与 POSIX 必须一致）。
	reordered := []FileRecord{files[1], files[0]}
	if got := workspaceVersionHash("w1", "builtin/3", 2, reordered); got != base {
		t.Errorf("file order changed the version: %q vs %q", got, base)
	}

	changedHash := []FileRecord{
		{Path: "pkg/b.go", ContentHash: "hash-b"},
		{Path: "pkg/a.go", ContentHash: "hash-a-changed"},
	}
	if got := workspaceVersionHash("w1", "builtin/3", 2, changedHash); got == base {
		t.Error("content_hash change did not change the version")
	}
	if got := workspaceVersionHash("w1", "builtin/4", 2, files); got == base {
		t.Error("adapter version change did not change the version")
	}
	if got := workspaceVersionHash("w1", "builtin/3", 3, files); got == base {
		t.Error("schema version change did not change the version")
	}
	if got := workspaceVersionHash("w2", "builtin/3", 2, files); got == base {
		t.Error("workspace change did not change the version")
	}
	if got := workspaceVersionHash("w1", "builtin/3", 2, append(files, FileRecord{Path: "pkg/c.go", ContentHash: "hash-c"})); got == base {
		t.Error("file set change did not change the version")
	}

	empty := workspaceVersionHash("w1", "builtin/3", 2, nil)
	if empty != workspaceVersionHash("w1", "builtin/3", 2, []FileRecord{}) {
		t.Error("nil and empty file slices produced different versions")
	}
	if empty == base {
		t.Error("empty file set shares the version of a non-empty set")
	}
}

// TestWorkspaceVersionTracksFileContent 是 store 端的集成验证：
// 文件内容哈希变化 / 软删除都会移动版本，同输入重复计算幂等。
func TestWorkspaceVersionTracksFileContent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	base, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion(empty): %v", err)
	}
	if again, err := WorkspaceVersion(ctx, store, wsID); err != nil || again != base {
		t.Fatalf("WorkspaceVersion not idempotent: %q vs %q (err=%v)", again, base, err)
	}

	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/a.go", ContentHash: "h1"}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	v1, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion(v1): %v", err)
	}
	if v1 == base {
		t.Error("adding a file did not change the version")
	}
	if again, err := WorkspaceVersion(ctx, store, wsID); err != nil || again != v1 {
		t.Fatalf("WorkspaceVersion not idempotent after write: %q vs %q (err=%v)", again, v1, err)
	}

	// 内容哈希不变的重写（同内容重复索引）不得移动版本。
	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/a.go", ContentHash: "h1"}); err != nil {
		t.Fatalf("UpsertFile(same hash): %v", err)
	}
	if same, err := WorkspaceVersion(ctx, store, wsID); err != nil || same != v1 {
		t.Fatalf("same content hash changed the version: %q vs %q (err=%v)", same, v1, err)
	}

	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/a.go", ContentHash: "h2"}); err != nil {
		t.Fatalf("UpsertFile(new hash): %v", err)
	}
	v2, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion(v2): %v", err)
	}
	if v2 == v1 {
		t.Error("content_hash change did not change the version")
	}

	if _, err := store.MarkFilesDeleted(ctx, wsID, []string{"pkg/a.go"}, time.Now()); err != nil {
		t.Fatalf("MarkFilesDeleted: %v", err)
	}
	v3, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion(v3): %v", err)
	}
	if v3 == v2 {
		t.Error("soft delete did not change the version")
	}

	if _, err := WorkspaceVersion(ctx, store, "  "); err == nil {
		t.Error("WorkspaceVersion with empty workspace_id succeeded, want error")
	}
	if _, err := WorkspaceVersion(ctx, nil, wsID); err == nil {
		t.Error("WorkspaceVersion with nil store succeeded, want error")
	}
}
