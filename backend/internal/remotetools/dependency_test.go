package remotetools

import "testing"

// 远程工具面的依赖闭包（§9.2 第 9 项）。
//
// 关键场景：custom profile 只允许 shell 且没有配置 jobs.root 时，job 工具不会
// 由 profile 自动带入。此时若不跑依赖闭包，模型会拿到一个"能超时 promoted 成
// job 引用、却读不到结果"的断链面。
func TestRemoteShellOnlyAutoExposesJobOutput(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileCustom
	cfg.Tools = []string{"shell"}
	// 关键：清空 jobs.root，使 job 工具不被 profile 自动带入，
	// 从而单独验证依赖闭包的效果。
	cfg.Jobs.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	names := map[string]bool{}
	for _, name := range cfg.enabledTools() {
		names[name] = true
	}
	if !names["shell"] {
		t.Fatalf("shell should be enabled, got %v", names)
	}
	if !names["job_output"] {
		t.Fatalf("job_output should be auto-exposed alongside shell, got %v", names)
	}
}

// 观测面：doctor / tools 列表需要能解释"为什么多出一个工具"。
//
// 用户写了 tools:["shell"] 却在 tools/list 看到 job_output 时，必须能从诊断
// 输出里看到它是由依赖补入的，而不是以为配置失效。
func TestRemoteAutoExposedToolsAreObservable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileCustom
	cfg.Tools = []string{"shell"}
	cfg.Jobs.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	auto := cfg.AutoExposedTools()
	found := false
	for _, name := range auto {
		if name == "job_output" {
			found = true
		}
	}
	if !found {
		t.Fatalf("job_output should be reported as auto-exposed, got %v", auto)
	}

	// EffectiveTools 必须包含 AutoExposedTools。
	effective := map[string]bool{}
	for _, name := range cfg.EffectiveTools() {
		effective[name] = true
	}
	for _, name := range auto {
		if !effective[name] {
			t.Fatalf("auto-exposed tool %q missing from effective surface %v", name, cfg.EffectiveTools())
		}
	}
}

// 无 producer 时观测面必须为空：不能凭空声称补入。
func TestRemoteAutoExposedEmptyWithoutProducer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileCustom
	cfg.Tools = []string{"view"}
	cfg.Jobs.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if auto := cfg.AutoExposedTools(); len(auto) != 0 {
		t.Fatalf("nothing should be auto-exposed without a producer, got %v", auto)
	}
}

// 无 shell 生产者时，依赖闭包不得凭空补入 job_output。
//
// 注意：Normalize 会把 Jobs.Root / Artifacts.Root 补成默认值，因此 custom
// profile 即使 tools 为空也会带出 job_* 与 artifact_read——那是既有 profile
// 语义，与依赖闭包无关。这里验证的是闭包本身不越界：只有当 shell 出现在
// 生效面时，job_output 才应因依赖被补入。
func TestRemoteNoShellDoesNotPullJobOutput(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileCustom
	cfg.Tools = []string{"view"}
	cfg.Jobs.Root = ""
	cfg.Artifacts.Root = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	_, resolution := cfg.resolveEnabledTools()
	if resolution.Failed() {
		t.Fatalf("unexpected dependency errors: %v", resolution.Errors)
	}
	for _, name := range resolution.AutoExposed {
		if name == "job_output" {
			t.Fatalf("job_output must not be auto-exposed without a shell producer, surface=%v", resolution.Tools)
		}
	}
}

// read-only profile 不含 shell，因此也不应凭空带入 job_output。
func TestRemoteReadOnlyHasNoJobOutput(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileReadonly
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for _, name := range cfg.enabledTools() {
		if name == "job_output" {
			t.Fatalf("read-only surface must not expose job_output, got %v", cfg.enabledTools())
		}
	}
}
