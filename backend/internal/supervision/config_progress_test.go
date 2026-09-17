package supervision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestProgressCheckIntervalClamping pins the P0-2 改动 3 floor: an explicitly
// configured interval below 30s is clamped up (with a host-side warning), while
// 0/negative stay at 0 so "default off" and the disable switch are never
// silently turned on by WithDefaults.
func TestProgressCheckIntervalClamping(t *testing.T) {
	require.Equal(t, 30*time.Second, MinProgressCheckInterval)

	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero stays disabled", 0, 0},
		{"negative stays disabled", -time.Second, 0},
		{"one second clamps to the floor", time.Second, MinProgressCheckInterval},
		{"one second below the floor clamps", MinProgressCheckInterval - time.Second, MinProgressCheckInterval},
		{"exactly the floor is unchanged", MinProgressCheckInterval, MinProgressCheckInterval},
		{"a normal interval is unchanged", 90 * time.Second, 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ProgressCheckInterval: tc.in}
			require.Equal(t, tc.want, cfg.WithDefaults().ProgressCheckInterval)
			// WithDefaults 不改原值：宿主仍能读到用户原始配置用于告警。
			require.Equal(t, tc.in, cfg.ProgressCheckInterval)
		})
	}
}

// TestProgressCheckEnabledSemantics locks the enable switch: only a positive
// configured value enables the sweep. A sub-floor value still counts as
// enabled (the host clamps it and warns), and turning it back to 0 keeps the
// historical "no ticker, no goroutine" behavior.
func TestProgressCheckEnabledSemantics(t *testing.T) {
	require.False(t, Config{}.ProgressCheckEnabled(), "zero config must not enable the sweep")
	require.False(t, Config{ProgressCheckInterval: -time.Minute}.ProgressCheckEnabled())
	require.True(t, Config{ProgressCheckInterval: time.Second}.ProgressCheckEnabled(),
		"a sub-floor value is enabled but clamped, never silently disabled")
	require.True(t, Config{ProgressCheckInterval: time.Minute}.ProgressCheckEnabled())
	require.True(t, Config{ProgressCheckInterval: time.Minute}.WithDefaults().ProgressCheckEnabled())
}

// TestSupervisionConfig_ProgressPassthrough covers the P0-2 P0-1 knobs reaching
// their consumers: WakeMaxProgressWake is forwarded to the wake scheduler, and
// TaskProgressInterval survives WithDefaults (0 stays off during the gray
// release; an explicit window is preserved).
func TestSupervisionConfig_ProgressPassthrough(t *testing.T) {
	defaults := Config{}.WakeSchedulerConfig()
	require.Zero(t, defaults.MaxProgressWakePerWindow,
		"0 is forwarded so the scheduler applies its own default (6)")

	require.Equal(t, 9, Config{WakeMaxProgressWake: 9}.WakeSchedulerConfig().MaxProgressWakePerWindow)
	require.Equal(t, -1, Config{WakeMaxProgressWake: -1}.WakeSchedulerConfig().MaxProgressWakePerWindow,
		"a negative value is forwarded as the documented unlimited escape hatch")

	require.Zero(t, Config{}.WithDefaults().TaskProgressInterval,
		"the task-level progress write-back is opt-in and stays off by default")
	require.Equal(t, 2*time.Minute, Config{TaskProgressInterval: 2 * time.Minute}.WithDefaults().TaskProgressInterval)
	require.Zero(t, Config{TaskProgressInterval: -time.Minute}.WithDefaults().TaskProgressInterval,
		"a negative window must not enable the write-back")
}

// TestProgressConfig_YAMLBinding pins the shipped key names from plan §3.2
// 改动 3, so a config sample cannot drift from the struct tags.
func TestProgressConfig_YAMLBinding(t *testing.T) {
	var cfg Config
	raw := []byte(`
progress_check_interval: 45s
wake_max_progress_wake: 3
task_progress_interval: 2m
`)
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	require.Equal(t, 45*time.Second, cfg.ProgressCheckInterval)
	require.Equal(t, 3, cfg.WakeMaxProgressWake)
	require.Equal(t, 2*time.Minute, cfg.TaskProgressInterval)
	require.Equal(t, 3, cfg.WakeSchedulerConfig().MaxProgressWakePerWindow)

	// 两档键名（supervision.progress_check_interval / wake_max_progress_wake）
	// 是 snake_case；写错大小写会被静默忽略，这里用未配置的键钉住默认关闭。
	var untouched Config
	require.NoError(t, yaml.Unmarshal([]byte("wake_rate_window: 30m\n"), &untouched))
	require.Zero(t, untouched.ProgressCheckInterval)
	require.False(t, untouched.ProgressCheckEnabled())
}
