package main

import (
	"os"
	"testing"
)

// TestParseMemoryLimit 覆盖单位解析的边界：二进制/十进制/纯字节/非法值/溢出。
func TestParseMemoryLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"512MiB", 512 << 20, true},
		{"512mib", 512 << 20, true},
		{"1GiB", 1 << 30, true},
		{"512MB", 512_000_000, true},
		{"1GB", 1_000_000_000, true},
		{"256KiB", 256 << 10, true},
		{"1024", 1024, true},
		{"1024B", 1024, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-5", 0, false},
		{"0", 0, false},
		{"9999999TiB", 0, false}, // 溢出保护
	}
	for _, tc := range cases {
		got, ok := parseMemoryLimit(tc.raw)
		if ok != tc.ok {
			t.Fatalf("parseMemoryLimit(%q) ok=%v, want %v", tc.raw, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Fatalf("parseMemoryLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// TestDisabledMemoryLimit 锁定退出开关的写法集合。
func TestDisabledMemoryLimit(t *testing.T) {
	for _, raw := range []string{"off", "OFF", "none", "disable", "0", "-1"} {
		if !disabledMemoryLimit(raw) {
			t.Fatalf("disabledMemoryLimit(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"512MiB", "1GiB", ""} {
		if disabledMemoryLimit(raw) {
			t.Fatalf("disabledMemoryLimit(%q) = true, want false", raw)
		}
	}
}

// TestApplyMemoryLimitDefaultsWhenUnset 验证未配置时启用默认软上限。
func TestApplyMemoryLimitDefaultsWhenUnset(t *testing.T) {
	for _, name := range memoryLimitEnvVars {
		t.Setenv(name, "")
	}
	limit := applyMemoryLimitFromEnv()
	if limit != defaultMemoryLimitBytes {
		t.Fatalf("limit = %d, want %d", limit, defaultMemoryLimitBytes)
	}
}

// TestApplyMemoryLimitHonorsAICLIVar 验证项目专用变量被解析并生效。
func TestApplyMemoryLimitHonorsAICLIVar(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("AICLI_MEMORY_LIMIT", "384MiB")
	limit := applyMemoryLimitFromEnv()
	if limit != 384<<20 {
		t.Fatalf("limit = %d, want %d", limit, 384<<20)
	}
}

// TestApplyMemoryLimitDefersToGoMemLimit 验证用户设置 GOMEMLIMIT 时不再覆盖：
// runtime 自己解析官方变量，本函数不应重复 SetMemoryLimit。
func TestApplyMemoryLimitDefersToGoMemLimit(t *testing.T) {
	t.Setenv("AICLI_MEMORY_LIMIT", "")
	t.Setenv("GOMEMLIMIT", "700MiB")
	if limit := applyMemoryLimitFromEnv(); limit != 0 {
		t.Fatalf("limit = %d, want 0 (deferred to runtime)", limit)
	}
	_ = os.Unsetenv("GOMEMLIMIT")
}

// TestApplyMemoryLimitOffDisables 验证显式 off 不设上限。
func TestApplyMemoryLimitOffDisables(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("AICLI_MEMORY_LIMIT", "off")
	if limit := applyMemoryLimitFromEnv(); limit != 0 {
		t.Fatalf("limit = %d, want 0 (disabled)", limit)
	}
}
