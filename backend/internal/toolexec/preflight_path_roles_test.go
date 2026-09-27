package toolexec

import (
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func pathPreflightSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":   map[string]interface{}{"type": "string"},
			"source_path": map[string]interface{}{"type": "string"},
		},
	}
}

func safeRetryMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataRetryClassKey: runtimetypes.ToolRetryClassSafe,
	}
}

// TestApplyPreflightSkipsLocalProbeForRemoteFSOwner 锁定 P1-4：MCP 工具服务端/
// 浏览器等非 runtime 归属的路径不做本机 Stat、不枚举候选，也不会因为本机不存在
// 而被误拒（截图/快照输出场景）。
func TestApplyPreflightSkipsLocalProbeForRemoteFSOwner(t *testing.T) {
	probes := 0
	metadata := safeRetryMetadata()
	metadata[runtimetypes.ToolMetadataFSOwnerKey] = runtimetypes.ToolFSOwnerToolServer
	decision := ApplyPreflight(nil, PreflightRequest{
		ToolName:    "browser_take_screenshot",
		Args:        map[string]interface{}{"file_path": "shot.png"},
		InputSchema: pathPreflightSchema(),
		Metadata:    metadata,
		PathExists:  func(string) bool { probes++; return false },
		PathProbe:   func(string) PathProbeResult { probes++; return PathProbeMissing },
	})
	if !decision.Allow {
		t.Fatalf("remote-owned path must not be denied locally: %+v", decision)
	}
	if probes != 0 {
		t.Fatalf("remote-owned paths must not be probed locally, probes=%d", probes)
	}
	if len(decision.PathCandidates) != 0 {
		t.Fatalf("remote-owned paths must not enumerate local candidates: %v", decision.PathCandidates)
	}
}

// TestApplyPreflightAllowsMissingOutputAndInOutLeaves 锁定 P1-4：本地输出/inout
// 角色允许叶子不存在（只校验父目录与写权限由沙箱负责），且不触发本机探测或
// 自动改写。
func TestApplyPreflightAllowsMissingOutputAndInOutLeaves(t *testing.T) {
	for _, role := range []string{runtimetypes.ToolPathRoleOutput, runtimetypes.ToolPathRoleInOut} {
		t.Run(role, func(t *testing.T) {
			probes := 0
			metadata := safeRetryMetadata()
			metadata[runtimetypes.ToolMetadataPathRolesKey] = map[string]interface{}{
				"file_path": role,
			}
			decision := ApplyPreflight(nil, PreflightRequest{
				ToolName:    "export_tool",
				Args:        map[string]interface{}{"file_path": "reports/new.csv"},
				InputSchema: pathPreflightSchema(),
				Metadata:    metadata,
				PathProbe:   func(string) PathProbeResult { probes++; return PathProbeMissing },
			})
			if !decision.Allow {
				t.Fatalf("%s leaf must be allowed to not exist yet: %+v", role, decision)
			}
			if probes != 0 {
				t.Fatalf("%s role must not be probed, probes=%d", role, probes)
			}
			if decision.PathAutoHealed || len(decision.PathCandidates) != 0 {
				t.Fatalf("%s role must never be auto-rewritten: %+v", role, decision)
			}
		})
	}
}

// TestApplyPreflightOutputRoleDoesNotMaskOtherInputs 锁定 P1-4：角色过滤是逐参数
// 的，声明的输出角色不能顺带放过同一调用里的本地输入缺失。
func TestApplyPreflightOutputRoleDoesNotMaskOtherInputs(t *testing.T) {
	metadata := safeRetryMetadata()
	metadata[runtimetypes.ToolMetadataPathRolesKey] = map[string]interface{}{
		"file_path": runtimetypes.ToolPathRoleOutput,
	}
	decision := ApplyPreflight(nil, PreflightRequest{
		ToolName:    "convert_tool",
		Args:        map[string]interface{}{"file_path": "out.csv", "source_path": "missing.csv"},
		InputSchema: pathPreflightSchema(),
		Metadata:    metadata,
		PathProbe:   func(path string) PathProbeResult { return PathProbeMissing },
	})
	if decision.Allow {
		t.Fatalf("a missing declared input must still be denied: %+v", decision)
	}
	if decision.ErrorCode != string(runtimeerrors.ErrToolPathNotFound) {
		t.Fatalf("error_code=%q want %s", decision.ErrorCode, runtimeerrors.ErrToolPathNotFound)
	}
	if !strings.Contains(decision.Error, "missing.csv") {
		t.Fatalf("denial must name the missing input, got %q", decision.Error)
	}
}

// TestApplyPreflightInputRoleStaysFailClosed 锁定 P1-4：本地输入角色保持
// fail-closed，不会被新的角色通道放宽。
func TestApplyPreflightInputRoleStaysFailClosed(t *testing.T) {
	metadata := safeRetryMetadata()
	metadata[runtimetypes.ToolMetadataPathRolesKey] = map[string]interface{}{
		"file_path": runtimetypes.ToolPathRoleInput,
	}
	decision := ApplyPreflight(nil, PreflightRequest{
		ToolName:    "view",
		Args:        map[string]interface{}{"file_path": "missing.txt"},
		InputSchema: pathPreflightSchema(),
		Metadata:    metadata,
		PathProbe:   func(string) PathProbeResult { return PathProbeMissing },
	})
	if decision.Allow {
		t.Fatalf("missing local input must stay denied: %+v", decision)
	}
	if decision.ErrorCode != string(runtimeerrors.ErrToolPathNotFound) {
		t.Fatalf("error_code=%q want %s", decision.ErrorCode, runtimeerrors.ErrToolPathNotFound)
	}
	if decision.Retryable {
		t.Fatalf("path miss must stay non-retryable: %+v", decision)
	}
}

// TestApplyPreflightPermissionFailureUsesDedicatedCode 锁定 P1-4 item 4：
// os.ErrNotExist 之外的本机探测失败（权限/IO）不能报成 TOOL_PATH_NOT_FOUND，
// 也不能给出候选或自动改写。
func TestApplyPreflightPermissionFailureUsesDedicatedCode(t *testing.T) {
	metadata := safeRetryMetadata()
	metadata[runtimetypes.ToolMetadataPathRolesKey] = map[string]interface{}{
		"file_path": runtimetypes.ToolPathRoleInput,
	}
	decision := ApplyPreflight(nil, PreflightRequest{
		ToolName:    "view",
		Args:        map[string]interface{}{"file_path": "locked.txt"},
		InputSchema: pathPreflightSchema(),
		Metadata:    metadata,
		PathProbe:   func(string) PathProbeResult { return PathProbeIndeterminate },
	})
	if decision.Allow {
		t.Fatalf("indeterminate path must be denied: %+v", decision)
	}
	if decision.ErrorCode != string(runtimeerrors.ErrToolPathAccessFailed) {
		t.Fatalf("error_code=%q want %s", decision.ErrorCode, runtimeerrors.ErrToolPathAccessFailed)
	}
	if decision.Preflight != "path_access" {
		t.Fatalf("preflight=%q want path_access", decision.Preflight)
	}
	if decision.Retryable {
		t.Fatalf("permission failure must not be retryable unchanged: %+v", decision)
	}
	if len(decision.PathCandidates) != 0 || decision.PathAutoHealed {
		t.Fatalf("permission failures must not offer candidates or rewrites: %+v", decision)
	}
	if !strings.Contains(decision.NextAction, "permission") {
		t.Fatalf("next_action must explain the permission class, got %q", decision.NextAction)
	}
}

// TestStatPathProbeClassifiesPermissionErrors 锁定探测分类本身：只有
// fs.ErrNotExist 是 missing，其余错误是 indeterminate。
func TestStatPathProbeClassifiesPermissionErrors(t *testing.T) {
	if got := statPathProbe(""); got != PathProbeMissing {
		t.Fatalf("empty path probe=%v want missing", got)
	}
	if got := statPathProbe(t.TempDir()); got != PathProbeExists {
		t.Fatalf("existing dir probe=%v want exists", got)
	}
	missing := t.TempDir() + string([]rune{'/', 'n', 'o', 'p', 'e'})
	if got := statPathProbe(missing); got != PathProbeMissing {
		t.Fatalf("missing path probe=%v want missing", got)
	}
}

// TestRewritePathLikeArgsRefusesNonInputAndRemoteRoles 锁定 P1-4 item 5：
// 自动改写只允许 runtime 归属的 input 角色，输出/工作目录/远端路径一律不改写。
func TestRewritePathLikeArgsRefusesNonInputAndRemoteRoles(t *testing.T) {
	outputMeta := map[string]interface{}{
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleOutput,
		},
	}
	args := map[string]interface{}{"file_path": "a.txt"}
	if rewritePathLikeArgs(args, outputMeta, "a.txt", "b.txt") {
		t.Fatalf("output role must not be auto-rewritten: %#v", args)
	}
	if args["file_path"] != "a.txt" {
		t.Fatalf("output role args must stay unchanged: %#v", args)
	}

	inputMeta := map[string]interface{}{
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleInput,
		},
	}
	if !rewritePathLikeArgs(args, inputMeta, "a.txt", "b.txt") {
		t.Fatalf("input role must stay auto-rewritable: %#v", args)
	}
	if args["file_path"] != "b.txt" {
		t.Fatalf("input role rewrite failed: %#v", args)
	}

	remoteArgs := map[string]interface{}{"file_path": "a.txt"}
	remoteMeta := map[string]interface{}{
		runtimetypes.ToolMetadataFSOwnerKey: runtimetypes.ToolFSOwnerToolServer,
	}
	if rewritePathLikeArgs(remoteArgs, remoteMeta, "a.txt", "b.txt") {
		t.Fatalf("remote-owned paths must never be rewritten: %#v", remoteArgs)
	}
}

// TestApplyPreflightRevalidatesAutoHealedPath 锁定 P1-4 item 5：自动改写后的参数
// 必须再走一遍策略/沙箱校验；校验通过才允许带新路径执行。
func TestApplyPreflightRevalidatesAutoHealedPath(t *testing.T) {
	dir := t.TempDir()
	realName := "config.yaml"
	realPath := filepath.Join(dir, realName)
	if err := os.WriteFile(realPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	missing := filepath.Join(dir, "config.yam")
	args := map[string]interface{}{"file_path": missing}

	validated := 0
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: pathPreflightSchema(),
		Metadata:    safeRetryMetadata(),
		PathExists: func(path string) bool {
			return path == realPath || strings.HasSuffix(path, realName)
		},
		PathRewriteValidator: func(candidate map[string]interface{}) error {
			validated++
			if got, _ := candidate["file_path"].(string); !strings.HasSuffix(got, realName) {
				t.Fatalf("validator must see the healed args, got %#v", candidate)
			}
			return nil
		},
	})
	if !decision.Allow || !decision.PathAutoHealed {
		t.Fatalf("expected validated auto-heal to allow, got %+v", decision)
	}
	if validated != 1 {
		t.Fatalf("rewrite must be re-validated exactly once, got %d", validated)
	}
}

// TestApplyPreflightRevertsAutoHealRejectedByPolicy 锁定 P1-4 item 5：校验失败的
// 自动改写必须回滚参数并保持拒绝，不能带着未校验的路径执行。
func TestApplyPreflightRevertsAutoHealRejectedByPolicy(t *testing.T) {
	dir := t.TempDir()
	realName := "config.yaml"
	realPath := filepath.Join(dir, realName)
	if err := os.WriteFile(realPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	missing := filepath.Join(dir, "config.yam")
	args := map[string]interface{}{"file_path": missing}

	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: pathPreflightSchema(),
		Metadata:    safeRetryMetadata(),
		PathExists: func(path string) bool {
			return path == realPath || strings.HasSuffix(path, realName)
		},
		PathRewriteValidator: func(map[string]interface{}) error {
			return stderrors.New("path outside sandbox: " + realPath)
		},
	})
	if decision.Allow {
		t.Fatalf("policy-rejected auto-heal must not allow: %+v", decision)
	}
	if decision.PathAutoHealed {
		t.Fatalf("path_auto_healed must stay false after revert: %+v", decision)
	}
	if decision.ErrorCode != string(runtimeerrors.ErrToolPathNotFound) {
		t.Fatalf("error_code=%q want %s", decision.ErrorCode, runtimeerrors.ErrToolPathNotFound)
	}
	if got, _ := args["file_path"].(string); got != missing {
		t.Fatalf("args must be reverted to the original path, got %q", got)
	}
	if !strings.Contains(decision.Error, "auto-heal rejected by policy") {
		t.Fatalf("denial must explain the rejected rewrite, got %q", decision.Error)
	}
	if !strings.Contains(decision.NextAction, "outside the execution boundary") {
		t.Fatalf("next_action must point at the boundary, got %q", decision.NextAction)
	}
}
