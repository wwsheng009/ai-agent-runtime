package policy

import "testing"

// 条件性契约（结果契约依赖的授权落地面）测试。
//
// 与无条件契约（Entry.Contractual，例如 artifact_read）共用 ContractualTools
// 这一条授权路径，但语义不同：条件性契约由调用方按工具面推断后授予，见
// docs/plan/tool-surface-dependency-auto-exposure-design-20261029.md §3.4。
func TestGrantConditionalContractualTools(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	if err := policy.AllowTool("job_output"); err == nil {
		t.Fatal("job_output should not be allowed before the grant")
	}

	policy.GrantConditionalContractualTools("job_output")
	if err := policy.AllowTool("job_output"); err != nil {
		t.Fatalf("job_output should be allowed after the conditional grant: %v", err)
	}
}

// deny 恒优先：显式拒绝的名字不得被条件性契约授予。
func TestGrantConditionalContractualToolsRespectsDeny(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	policy.DeniedTools = map[string]bool{"job_output": true}

	policy.GrantConditionalContractualTools("job_output")
	if policy.ContractualTools["job_output"] {
		t.Fatal("denied tool must not enter the contractual set")
	}
	if err := policy.AllowTool("job_output"); err == nil {
		t.Fatal("denied tool must stay denied")
	}
}

// 契约只填补遗漏项，不重启被完全关闭的面。
func TestGrantConditionalContractualToolsEmptyAllowlist(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{}, false)
	policy.AllowlistEnabled = true
	policy.AllowedTools = map[string]bool{}

	policy.GrantConditionalContractualTools("job_output")
	if err := policy.AllowTool("job_output"); err == nil {
		t.Fatal("contract must not re-enable a deliberately empty surface")
	}
}

// 别名归一：用别名授予也应落到 canonical 名字上。
func TestGrantConditionalContractualToolsNormalizesAlias(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"shell"}, false)
	policy.GrantConditionalContractualTools("task_output")
	if !policy.ContractualTools["job_output"] {
		t.Fatalf("alias should normalize to job_output, got %v", policy.ContractualTools)
	}
}

// 无条件契约不受条件性授予影响。
func TestGrantConditionalContractualToolsKeepsUnconditional(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "shell"}, false)
	if err := policy.AllowTool("artifact_read"); err != nil {
		t.Fatalf("artifact_read is an unconditional contract and should be allowed: %v", err)
	}
	policy.GrantConditionalContractualTools("job_output")
	if err := policy.AllowTool("artifact_read"); err != nil {
		t.Fatalf("unconditional contract must survive a conditional grant: %v", err)
	}
}
