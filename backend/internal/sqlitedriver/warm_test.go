package sqlitedriver

import (
	"testing"
	"time"
)

// WarmAsync 的开关是 A/B 对比与排障的公共契约（docs/e2e/resume-m1-ab-measurement.md），
// 取值判定必须是「只有明确关闭才关闭」，不能因为环境里有垃圾值就把预热关掉。
func TestSQLiteWarmupDisabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"1", false},
		{"true", false},
		{"yes", false},
		{"on", false},
		{"0", true},
		{"false", true},
		{"FALSE", true},
		{"False", true},
		{"no", true},
		{"off", true},
		{" off ", true},
		{"\tOFF\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("AICLI_SQLITE_WARMUP", tc.value)
			if got := sqliteWarmupDisabled(); got != tc.want {
				t.Fatalf("AICLI_SQLITE_WARMUP=%q: got disabled=%v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// 关闭时必须立刻返回：预热是「后台尽力而为」，任何情况下都不允许阻塞启动路径。
func TestWarmAsyncReturnsPromptlyWhenDisabled(t *testing.T) {
	t.Setenv("AICLI_SQLITE_WARMUP", "0")
	done := make(chan struct{})
	go func() {
		WarmAsync()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WarmAsync blocked although warmup is disabled via AICLI_SQLITE_WARMUP=0")
	}
}
