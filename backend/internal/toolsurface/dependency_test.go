package toolsurface

import (
	"strings"
	"testing"
	"time"
)

// Tests for the result-contract dependency closure (§9.1 of
// docs/plan/tool-surface-dependency-auto-exposure-design-20261029.md).
//
// The core invariant under test: shell never calls job_output, but a promoted
// shell result is a job reference, so a surface that exposes shell without
// job_output is broken.

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// 1. shell -> job_output 自动加入。
func TestResolveDependenciesShellAutoExposesJobOutput(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell"}})
	if res.Failed() {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if !contains(res.Tools, "job_output") {
		t.Fatalf("job_output should be auto-exposed, got %v", res.Tools)
	}
	if !contains(res.AutoExposed, "job_output") {
		t.Fatalf("job_output should be reported as auto-exposed, got %v", res.AutoExposed)
	}
}

// 2. job_start -> job_output 自动加入。
func TestResolveDependenciesJobStartAutoExposesJobOutput(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"job_start"}})
	if res.Failed() {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if !contains(res.Tools, "job_output") {
		t.Fatalf("job_output should be auto-exposed, got %v", res.Tools)
	}
}

// 3. job_output 不反向引入 shell/job_start。
func TestResolveDependenciesJobOutputPullsNothing(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"job_output"}})
	if len(res.AutoExposed) != 0 {
		t.Fatalf("job_output must not auto-expose anything, got %v", res.AutoExposed)
	}
	if contains(res.Tools, "shell") || contains(res.Tools, "job_start") {
		t.Fatalf("job_output must not pull producers, got %v", res.Tools)
	}
}

// 4. shell + deny job_output -> required error 且记录断链生产者。
func TestResolveDependenciesDeniedRequiredIsError(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{
		Base:   []string{"shell"},
		Denied: []string{"job_output"},
	})
	if !res.Failed() {
		t.Fatal("denied required dependency must fail closed")
	}
	if !contains(res.BrokenProducers, "shell") {
		t.Fatalf("shell should be reported as a broken producer, got %v", res.BrokenProducers)
	}
	if contains(res.Tools, "job_output") {
		t.Fatalf("denied tool must not appear in the surface, got %v", res.Tools)
	}
}

// 5. shell + readOnly -> shell 不存在，不自动加 job_output。
func TestResolveDependenciesReadOnlyDropsShell(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{
		Base:     []string{"shell"},
		ReadOnly: true,
	})
	if contains(res.Tools, "shell") {
		t.Fatalf("shell must not survive a read-only surface, got %v", res.Tools)
	}
	if contains(res.Tools, "job_output") {
		t.Fatalf("no producer means no consumer, got %v", res.Tools)
	}
	if res.Failed() {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
}

// 6. custom tools:["shell"] -> closure 加入 job_output（等价于已按 profile 裁剪的 Base）。
func TestResolveDependenciesCustomAllowlistCloses(t *testing.T) {
	base := []string{"shell", "grep", "view"}
	res := ResolveDependencies(SurfaceResolutionInput{Base: base})
	if !contains(res.Tools, "job_output") {
		t.Fatalf("custom allowlist should be closed over, got %v", res.Tools)
	}
	for _, want := range base {
		if !contains(res.Tools, want) {
			t.Fatalf("base tool %q lost, got %v", want, res.Tools)
		}
	}
}

// 7. alias task_output 归一后识别为 job_output。
func TestResolveDependenciesAliasNormalized(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell", "task_output"}})
	if res.Failed() {
		t.Fatalf("alias should satisfy the dependency, got %v", res.Errors)
	}
	if len(res.AutoExposed) != 0 {
		t.Fatalf("alias already present, nothing to auto-expose, got %v", res.AutoExposed)
	}
	if !contains(res.Tools, "job_output") {
		t.Fatalf("alias should normalize to job_output, got %v", res.Tools)
	}
}

// 8. 幂等：重复 resolve 不重复 auto-expose / 不重复告警。
func TestResolveDependenciesIdempotent(t *testing.T) {
	first := ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell"}})
	second := ResolveDependencies(SurfaceResolutionInput{Base: first.Tools})
	if len(second.AutoExposed) != 0 {
		t.Fatalf("second pass should auto-expose nothing, got %v", second.AutoExposed)
	}
	if len(second.Tools) != len(first.Tools) {
		t.Fatalf("surface changed across passes: %v vs %v", first.Tools, second.Tools)
	}
}

// 9. 无环：闭包必须终止（shell -> job_output 无回边）。
func TestResolveDependenciesTerminates(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell", "job_start"}})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dependency closure did not terminate: cycle in declarations")
	}
}

// 10. aicli_exec 不产生任何依赖（钉死 §3.2.1 的结论）。
//
// 它是 ShellLike 但不是 producer：超时直接失败、不返回 job id。若后人按
// ShellLike 给它加依赖，本测试会失败。
func TestResolveDependenciesAicliExecIsNotProducer(t *testing.T) {
	if deps := DependenciesOf("aicli_exec"); len(deps) != 0 {
		t.Fatalf("aicli_exec must not declare dependencies, got %v", deps)
	}
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"aicli_exec"}})
	if len(res.AutoExposed) != 0 {
		t.Fatalf("aicli_exec must not auto-expose anything, got %v", res.AutoExposed)
	}
	if contains(res.Tools, "job_output") {
		t.Fatalf("aicli_exec must not pull job_output, got %v", res.Tools)
	}
}

// 11. recommended 缺失只告警、不阻断；且不指向不存在的工具。
func TestResolveDependenciesRecommendedOnlyWarns(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell"}})
	if res.Failed() {
		t.Fatalf("recommended gaps must not fail, got %v", res.Errors)
	}
	// job_cancel / job_list 是 recommended，应出现在告警里。
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "job_cancel") {
		t.Fatalf("expected a job_cancel recommendation warning, got %v", res.Warnings)
	}
	// job_search 在 remote 面没有实现，声明里不得出现（§3.2.2）。
	for _, dep := range DependenciesOf("job_start") {
		if dep.Tool == "job_search" {
			t.Fatal("job_start must not recommend job_search: not implemented on the remote surface")
		}
	}
}

// 12. Contractual 与 AutoExpose 互不干扰。
//
// artifact_read 是无条件契约（Entry.Contractual），job_output 是条件性契约。
// 两者共用 ContractualTools 这一条授权路径，但一方不得改写另一方的语义。
func TestResolveContractualToolsCoexistsWithUnconditionalContract(t *testing.T) {
	existing := map[string]bool{"artifact_read": true}
	granted, res := ResolveContractualTools(existing, SurfaceResolutionInput{Base: []string{"shell"}})
	if res.Failed() {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if !granted["artifact_read"] {
		t.Fatal("unconditional contract must be preserved")
	}
	if !granted["job_output"] {
		t.Fatal("conditional contract should be added")
	}

	// 没有 producer 时不得凭空加入条件性契约。
	none, _ := ResolveContractualTools(existing, SurfaceResolutionInput{Base: []string{"grep"}})
	if none["job_output"] {
		t.Fatal("job_output must not be granted without a producer")
	}
	if !none["artifact_read"] {
		t.Fatal("unconditional contract must survive an unrelated surface")
	}
}

// Disabled（DisableTools）不得被依赖闭包重启：契约只填补遗漏项。
func TestResolveDependenciesDisabledStaysEmpty(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{Base: []string{"shell"}, Disabled: true})
	if len(res.Tools) != 0 {
		t.Fatalf("disabled surface must stay empty, got %v", res.Tools)
	}
}

// LiteralNames：remote 语义下保留字面名，只补入依赖声明的字面名。
func TestResolveDependenciesLiteralNames(t *testing.T) {
	res := ResolveDependencies(SurfaceResolutionInput{
		Base:         []string{"shell"},
		LiteralNames: true,
	})
	if !contains(res.Tools, "job_output") {
		t.Fatalf("literal mode should still close over the dependency, got %v", res.Tools)
	}
}
