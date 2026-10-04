package sessionmeta

import "testing"

func TestCopyParentWorkspaceBindingFillsBlankChild(t *testing.T) {
	src := map[string]interface{}{WorkspacePath: "/repos/main"}
	dst := map[string]interface{}{}

	if !CopyParentWorkspaceBinding(&dst, src) {
		t.Fatal("expected the parent workspace to be written into an unbound child")
	}
	if got := String(dst, WorkspacePath); got != "/repos/main" {
		t.Fatalf("workspace_path = %q, want /repos/main", got)
	}
}

func TestCopyParentWorkspaceBindingDoesNotOverwriteBoundChild(t *testing.T) {
	src := map[string]interface{}{WorkspacePath: "/repos/main"}
	dst := map[string]interface{}{WorkspacePath: "/repos/child-worktree"}

	if CopyParentWorkspaceBinding(&dst, src) {
		t.Fatal("a child with its own bound directory must not be overwritten")
	}
	if got := String(dst, WorkspacePath); got != "/repos/child-worktree" {
		t.Fatalf("workspace_path = %q, want the child binding to survive", got)
	}
}

func TestCopyParentWorkspaceBindingUnboundParentIsNoChange(t *testing.T) {
	var dst map[string]interface{}

	if CopyParentWorkspaceBinding(&dst, nil) {
		t.Fatal("an unbound parent has nothing to inherit")
	}
	if dst != nil {
		t.Fatalf("dst must stay nil when the parent has no workspace, got %#v", dst)
	}

	dst = map[string]interface{}{ProfileName: "coding"}
	if CopyParentWorkspaceBinding(&dst, map[string]interface{}{}) {
		t.Fatal("an empty parent context must not write anything")
	}
	if _, ok := dst[WorkspacePath]; ok {
		t.Fatal("no workspace key may be written when the parent is unbound")
	}
	if String(dst, ProfileName) != "coding" {
		t.Fatal("unrelated child keys must be left alone")
	}
}

func TestCopyParentWorkspaceBindingInitializesNilChildContext(t *testing.T) {
	var dst map[string]interface{}
	src := map[string]interface{}{WorkspacePath: "/repos/main"}

	if !CopyParentWorkspaceBinding(&dst, src) {
		t.Fatal("expected the write to happen")
	}
	if got := String(dst, WorkspacePath); got != "/repos/main" {
		t.Fatalf("workspace_path = %q, want /repos/main", got)
	}
}

// 继承只搬运 workspace_path，不得顺手把 profile 绑定一起带过去——两者的
// 收窄规则不同（CopyProfileBinding 负责 profile），混在一起会让「父未绑定
// profile」的不变量失效。
func TestCopyParentWorkspaceBindingDoesNotTouchProfileKeys(t *testing.T) {
	src := map[string]interface{}{
		WorkspacePath: "/repos/main",
		ProfileRef:    "/profiles/coding",
		ProfileName:   "coding",
	}
	dst := map[string]interface{}{}

	if !CopyParentWorkspaceBinding(&dst, src) {
		t.Fatal("expected the workspace write")
	}
	for _, key := range []string{ProfileRef, ProfileName, ProfileRoot, ProfileAgent} {
		if _, ok := dst[key]; ok {
			t.Fatalf("%s must not be copied by workspace inheritance", key)
		}
	}
}
