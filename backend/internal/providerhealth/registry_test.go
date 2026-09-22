package providerhealth

import (
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

var base = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }

// newTestRegistry 用确定性的小参数，便于一眼看出阈值边界。
func newTestRegistry() *Registry {
	return NewRegistry(Spec{
		FailureThreshold: 3,
		FailureRate:      0.5,
		SampleThreshold:  4,
		WindowDuration:   10 * time.Second,
		OpenTimeout:      60 * time.Second,
		HalfOpenMaxCalls: 1,
	})
}

func TestObserveTripsOnConsecutiveFailures(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 2; i++ {
		result := r.Observe("opencode.ai", "m", llm.FailureCategoryProviderError, at(i))
		if result.Tripped {
			t.Fatalf("第 %d 次失败不应触发熔断（阈值 3）", i+1)
		}
		if result.Opened {
			t.Fatalf("第 %d 次失败后电路不应打开", i+1)
		}
	}
	result := r.Observe("opencode.ai", "m", llm.FailureCategoryProviderError, at(2))
	if !result.Tripped {
		t.Fatal("第 3 次连续失败应触发熔断")
	}
	if result.Health.State != StateUnhealthy {
		t.Fatalf("熔断后状态应为 unhealthy，实际 %q", result.Health.State)
	}

	// 边沿语义：跨过阈值之后继续失败不应再报 Tripped。
	again := r.Observe("opencode.ai", "m", llm.FailureCategoryProviderError, at(3))
	if again.Tripped {
		t.Fatal("Tripped 是边沿触发，持续失败不应重复上报")
	}
	if !again.Opened {
		t.Fatal("持续失败期间电路应保持打开")
	}
}

func TestObserveTripsOnFailureRate(t *testing.T) {
	r := newTestRegistry()
	// 交替成功/失败：连续失败数永远到不了 3，只能靠失败率规则触发。
	// 前 3 个样本不足 SampleThreshold=4，不应触发。
	for i := 0; i < 4; i++ {
		var result ObserveResult
		if i%2 == 0 {
			result = r.Observe("p", "m", llm.FailureCategoryTimeout, at(i))
		} else {
			result = r.ObserveSuccess("p", "m", at(i))
		}
		if result.Tripped {
			t.Fatalf("样本 %d 时样本数不足 SampleThreshold，不应触发", i+1)
		}
	}
	// 第 5 个样本是失败：此时 5 样本 3 失败 = 60% >= 50%，应触发。
	result := r.Observe("p", "m", llm.FailureCategoryTimeout, at(4))
	if !result.Tripped {
		t.Fatalf("窗口失败率 60%% 应触发熔断，实际 samples=%d failures=%d",
			result.Health.Samples, result.Health.Failures)
	}
}

func TestFailureRateIgnoresSamplesOutsideWindow(t *testing.T) {
	r := newTestRegistry()
	// 窗口内制造失败但不足以熔断：2 失败 + 3 成功 = 40% < 50%，连续失败 2 < 3。
	r.Observe("p", "m", llm.FailureCategoryProviderError, at(0))
	r.ObserveSuccess("p", "m", at(1))
	r.Observe("p", "m", llm.FailureCategoryProviderError, at(2))
	r.ObserveSuccess("p", "m", at(3))
	r.ObserveSuccess("p", "m", at(4))

	inWindow, known := r.Lookup("p", "m", at(5))
	if !known {
		t.Fatal("已有观测，known 应为 true")
	}
	if inWindow.Samples != 5 || inWindow.Failures != 2 {
		t.Fatalf("窗口内应见 5 样本 2 失败，实际 samples=%d failures=%d", inWindow.Samples, inWindow.Failures)
	}
	if inWindow.State != StateHealthy {
		t.Fatalf("40%% 失败率不应熔断，实际 %q", inWindow.State)
	}

	// 隔开足够久让旧样本滑出 10s 窗口：它们不得再计入失败率。
	expired, _ := r.Lookup("p", "m", at(100))
	if expired.Samples != 0 || expired.Failures != 0 {
		t.Fatalf("窗口外样本不应计入，实际 samples=%d failures=%d", expired.Samples, expired.Failures)
	}
	if expired.State != StateHealthy {
		t.Fatalf("未熔断且窗口内无样本时应为健康，实际 %q", expired.State)
	}
}

func TestOpenThenHalfOpenThenRecover(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 3; i++ {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	if health, _ := r.Lookup("p", "m", at(30)); health.State != StateUnhealthy {
		t.Fatalf("OpenTimeout=60s 内应保持打开，实际 %q", health.State)
	}

	// openedAt 停在 at(2)，因此 at(62) 恰好满 OpenTimeout=60s。
	health, _ := r.Lookup("p", "m", at(62))
	if health.State != StateDegraded {
		t.Fatalf("超过 OpenTimeout 应进入半开（degraded），实际 %q", health.State)
	}
	if health.Reason != "half_open_probe" {
		t.Fatalf("半开态 Reason 应为 half_open_probe，实际 %q", health.Reason)
	}

	// 半开探测成功 → 闭合，且连续失败清零。
	result := r.ObserveSuccess("p", "m", at(63))
	if result.Health.State != StateHealthy {
		t.Fatalf("半开探测成功后应闭合，实际 %q", result.Health.State)
	}
	if result.Health.ConsecutiveFailures != 0 {
		t.Fatalf("恢复后连续失败应清零，实际 %d", result.Health.ConsecutiveFailures)
	}
}

func TestHalfOpenProbeFailureReopens(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 3; i++ {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	// 半开期探测失败 → 重新打开，并重新计时。
	result := r.Observe("p", "m", llm.FailureCategoryProviderError, at(70))
	if !result.Opened || result.Health.State != StateUnhealthy {
		t.Fatalf("半开探测失败应重新打开，实际 state=%q opened=%v", result.Health.State, result.Opened)
	}
	// 重新计时后，再过 60s 才应再次进入半开。
	if health, _ := r.Lookup("p", "m", at(100)); health.State != StateUnhealthy {
		t.Fatalf("重新打开后 30s 内应保持打开，实际 %q", health.State)
	}
	if health, _ := r.Lookup("p", "m", at(130)); health.State != StateDegraded {
		t.Fatalf("重新计时满 60s 后应进入半开，实际 %q", health.State)
	}
}

// TestSustainedFailureDoesNotExtendOpenWindow 锁定一条关键语义：电路已打开后
// 持续失败不得推后 openedAt，否则 provider 永远进不了半开，也就永远无法恢复。
func TestSustainedFailureDoesNotExtendOpenWindow(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 3; i++ {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	// 在打开期内持续失败，一直持续到原本的恢复时刻之后。
	for i := 10; i < 70; i += 10 {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	// openedAt 停在 at(2)，因此 at(62) 恰好满 OpenTimeout=60s。
	if health, _ := r.Lookup("p", "m", at(62)); health.State != StateDegraded {
		t.Fatalf("持续失败不应推迟半开，now=62s 应为 degraded，实际 %q", health.State)
	}
}

// TestSuccessDuringOpenWindowDoesNotRecover 锁定：打开期内的成功不算恢复，
// 否则一次偶发成功就会把刚熔断的后端立刻放回来。
func TestSuccessDuringOpenWindowDoesNotRecover(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 3; i++ {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	r.ObserveSuccess("p", "m", at(10))
	if health, _ := r.Lookup("p", "m", at(11)); health.State != StateUnhealthy {
		t.Fatalf("打开期内的成功不应闭合电路，实际 %q", health.State)
	}
}

// TestNonProviderFaultsDoNotCount 是本包最重要的一条约束：自身 bug 与用户行为
// 绝不能把 provider 摘掉。
func TestNonProviderFaultsDoNotCount(t *testing.T) {
	for _, category := range []string{
		llm.FailureCategoryToolError,
		llm.FailureCategoryBudgetExceeded,
		llm.FailureCategoryContextOverflow,
		llm.FailureCategoryCancelled,
		llm.FailureCategoryUnknown,
		"",
		"PERMISSION_DENIED",
		"USER_CANCELLED",
		"TOOL_CALL_LIMIT_REACHED",
	} {
		t.Run(category, func(t *testing.T) {
			r := newTestRegistry()
			for i := 0; i < 20; i++ {
				result := r.Observe("p", "m", category, at(i))
				if result.Tripped || result.Opened {
					t.Fatalf("非 provider 归因失败（%q）不应触发熔断", category)
				}
				if result.Health.ConsecutiveFailures != 0 {
					t.Fatalf("非 provider 归因失败不应累积连续失败，实际 %d", result.Health.ConsecutiveFailures)
				}
				if result.Health.Samples != 0 {
					t.Fatalf("非 provider 归因失败不应进入样本窗口，实际 %d", result.Health.Samples)
				}
			}
		})
	}
}

func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	// 只保留连续失败规则：把失败率规则的样本门槛抬高，避免两条规则互相干扰。
	r := NewRegistry(Spec{FailureThreshold: 3, SampleThreshold: 1000})
	r.Observe("p", "m", llm.FailureCategoryTimeout, at(0))
	r.Observe("p", "m", llm.FailureCategoryTimeout, at(1))
	r.ObserveSuccess("p", "m", at(2))

	// 抖动过后重新计数：再连续两次失败不应触发（阈值 3）。
	for i := 3; i < 5; i++ {
		if result := r.Observe("p", "m", llm.FailureCategoryTimeout, at(i)); result.Tripped {
			t.Fatalf("成功清零后第 %d 次失败不应触发", i-2)
		}
	}
	if result := r.Observe("p", "m", llm.FailureCategoryTimeout, at(5)); !result.Tripped {
		t.Fatal("清零后重新累积到 3 次应触发")
	}
}

func TestLookupUnknownTarget(t *testing.T) {
	r := newTestRegistry()
	if _, known := r.Lookup("never-seen", "m", base); known {
		t.Fatal("无观测记录时 known 应为 false（调用方应视为健康）")
	}
}

func TestLookupIsSideEffectFree(t *testing.T) {
	r := newTestRegistry()
	for i := 0; i < 3; i++ {
		r.Observe("p", "m", llm.FailureCategoryProviderError, at(i))
	}
	// openedAt=at(2)，at(62) 恰好处于半开。
	before, _ := r.Lookup("p", "m", at(62))
	for i := 0; i < 5; i++ {
		r.Lookup("p", "m", at(62))
	}
	after, _ := r.Lookup("p", "m", at(62))
	if before != after {
		t.Fatalf("Lookup 不得推进状态机：before=%+v after=%+v", before, after)
	}
	if after.State != StateDegraded {
		t.Fatalf("重复读取不应改变半开判定，实际 %q", after.State)
	}
}

func TestSpecNormalization(t *testing.T) {
	spec := Spec{}.Normalized()
	if spec != DefaultSpec() {
		t.Fatalf("零值应回落到 DefaultSpec，实际 %+v", spec)
	}
	capped := Spec{FailureRate: 5}.Normalized()
	if capped.FailureRate != 1 {
		t.Fatalf("FailureRate>1 应截到 1，实际 %v", capped.FailureRate)
	}
	partial := Spec{FailureThreshold: 7, OpenTimeout: 5 * time.Second}.Normalized()
	if partial.FailureThreshold != 7 || partial.OpenTimeout != 5*time.Second {
		t.Fatal("显式配置的字段不应被缺省值覆盖")
	}
	if partial.WindowDuration != DefaultWindowDuration {
		t.Fatal("未配置的字段应回落到缺省值")
	}
}

func TestSnapshotsSortedAndComplete(t *testing.T) {
	r := newTestRegistry()
	r.Observe("zebra", "m", llm.FailureCategoryProviderError, at(0))
	r.ObserveSuccess("alpha", "m", at(0))
	r.ObserveSuccess("alpha", "a", at(0))

	snapshots := r.Snapshots(at(1))
	if len(snapshots) != 3 {
		t.Fatalf("应有 3 条快照，实际 %d", len(snapshots))
	}
	got := []string{
		snapshots[0].Provider + "/" + snapshots[0].Model,
		snapshots[1].Provider + "/" + snapshots[1].Model,
		snapshots[2].Provider + "/" + snapshots[2].Model,
	}
	want := []string{"alpha/a", "alpha/m", "zebra/m"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序不符：got=%v want=%v", got, want)
		}
	}
}

func TestProviderAndModelAreCaseInsensitive(t *testing.T) {
	r := newTestRegistry()
	r.Observe("OpenCode.AI", "Mimo-V2.5-Pro", llm.FailureCategoryProviderError, at(0))
	if _, known := r.Lookup("opencode.ai", "mimo-v2.5-pro", at(1)); !known {
		t.Fatal("provider/model 索引应大小写不敏感")
	}
}

func TestAppendSampleRespectsHardCap(t *testing.T) {
	samples := []observation{}
	window := time.Hour
	for i := 0; i < maxSamplesPerTarget+50; i++ {
		samples = appendSample(samples, observation{at: at(i), failure: true}, at(i), window)
	}
	if len(samples) > maxSamplesPerTarget {
		t.Fatalf("样本数应被硬上限兜住，实际 %d", len(samples))
	}
}

func TestConcurrentObserveAndLookup(t *testing.T) {
	r := newTestRegistry()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				now := at(i % 30)
				if i%3 == 0 {
					r.ObserveSuccess("p", "m", now)
				} else {
					r.Observe("p", "m", llm.FailureCategoryProviderError, now)
				}
				r.Lookup("p", "m", now)
				r.Snapshots(now)
			}
		}(worker)
	}
	wg.Wait()
}

func TestConfigureReplacesDefaultRegistry(t *testing.T) {
	original := Default()
	defer Configure(original.Spec())

	Default().Observe("p", "m", llm.FailureCategoryProviderError, base)
	Configure(Spec{FailureThreshold: 9})
	if _, known := Default().Lookup("p", "m", base); known {
		t.Fatal("Configure 应重建注册表并丢弃既有观测")
	}
	if Default().Spec().FailureThreshold != 9 {
		t.Fatalf("Configure 应生效，实际阈值 %d", Default().Spec().FailureThreshold)
	}
}

func TestClassifyFailureAcceptsCodesAndCategories(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// 已归一化的分类
		{llm.FailureCategoryProviderError, llm.FailureCategoryProviderError},
		{llm.FailureCategoryToolError, llm.FailureCategoryToolError},
		// llm 的稳定错误码（UPPER_SNAKE）
		{"UPSTREAM_UNAVAILABLE", llm.FailureCategoryProviderError},
		{"UPSTREAM_RATE_LIMITED", llm.FailureCategoryRateLimited},
		{"STREAM_INTERRUPTED", llm.FailureCategoryInterrupted},
		{"USER_CANCELLED", llm.FailureCategoryCancelled},
		{"CONTEXT_BUDGET_EXCEEDED", llm.FailureCategoryContextOverflow},
		{"PERMISSION_DENIED", llm.FailureCategoryToolError},
		// 别名
		{"rate_limit", llm.FailureCategoryRateLimited},
		{"deadline_exceeded", llm.FailureCategoryTimeout},
		// 无法判定：不得猜成具体类别
		{"", llm.FailureCategoryUnknown},
		{"SOMETHING_NEW", llm.FailureCategoryUnknown},
	}
	for _, testCase := range cases {
		if got := ClassifyFailure(testCase.raw); got != testCase.want {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}

func TestIsProviderFault(t *testing.T) {
	providerFaults := []string{
		llm.FailureCategoryProviderError,
		llm.FailureCategoryRateLimited,
		llm.FailureCategoryTimeout,
		llm.FailureCategoryInterrupted,
		"UPSTREAM_UNAVAILABLE",
		"CONNECTION_RESET",
	}
	for _, category := range providerFaults {
		if !IsProviderFault(category) {
			t.Errorf("%q 应归因于 provider", category)
		}
	}
	ourFaults := []string{
		llm.FailureCategoryToolError,
		llm.FailureCategoryBudgetExceeded,
		llm.FailureCategoryContextOverflow,
		llm.FailureCategoryCancelled,
		llm.FailureCategoryUnknown,
		"",
	}
	for _, category := range ourFaults {
		if IsProviderFault(category) {
			t.Errorf("%q 不应归因于 provider", category)
		}
	}
}
