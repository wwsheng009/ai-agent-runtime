package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// defaultMemoryLimitBytes 是未显式配置时使用的软上限。
//
// 依据：实测大会话（2,146 行 / 11.56 MB）冷启动峰值私有内存约 414 MB，而
// 稳定态存活堆只有 9–150 MB。把软上限设在 512 MiB 让运行时在接近峰值时就
// 开始归还，同时给单次装载留出足够头部空间。
//
// 之所以是"软"上限：Go 的 GOMEMLIMIT 不会因超限而终止进程，只会提高 GC
// 频率并让 scavenger 更积极地归还内存，因此设得偏低最坏只是多几次 GC。
const defaultMemoryLimitBytes int64 = 512 << 20 // 512 MiB

// memoryLimitEnvVars 是用户可用来覆盖软上限的环境变量，按优先级排列。
//
// 同时接受 GOMEMLIMIT（Go 官方变量）与 AICLI_MEMORY_LIMIT（本项目专用，
// 支持 "512MiB" / "512MB" / 纯字节数三种写法）。官方变量由 runtime 自己解析，
// 这里读取它只是为了"用户已设置则不再覆盖"。
var memoryLimitEnvVars = []string{"AICLI_MEMORY_LIMIT", "GOMEMLIMIT"}

// applyMemoryLimitFromEnv 设置运行时软内存上限。
//
// 语义：
//   - 用户显式配置了任一环境变量 → 尊重用户值（不覆盖运行时行为）；
//   - 显式配置为 "off"/"none"/"0"/"-1" → 不设上限（退出开关）；
//   - 都未配置 → 使用 defaultMemoryLimitBytes。
//
// 返回实际生效的上限字节数（0 表示未设置），便于测试与诊断输出。
func applyMemoryLimitFromEnv() int64 {
	for _, name := range memoryLimitEnvVars {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		if disabledMemoryLimit(raw) {
			// 显式关闭：清掉上限。
			debug.SetMemoryLimit(-1)
			return 0
		}
		// Go 官方变量由 runtime 自行解析；本函数不重复解析，避免与
		// runtime 的语义（含小数、多种单位）产生分歧。
		if name == "GOMEMLIMIT" {
			return 0
		}
		if parsed, ok := parseMemoryLimit(raw); ok {
			debug.SetMemoryLimit(parsed)
			return parsed
		}
		// 无法解析：不静默改成默认值（可能违背用户意图），保持 runtime 现状。
		return 0
	}
	debug.SetMemoryLimit(defaultMemoryLimitBytes)
	return defaultMemoryLimitBytes
}

// disabledMemoryLimit 报告该配置是否表示"关闭上限"。
func disabledMemoryLimit(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off", "none", "disable", "disabled", "0", "-1":
		return true
	}
	return false
}

// parseMemoryLimit 解析 "512MiB" / "512MB" / "1GiB" / 纯字节数。
//
// 只接受二进制（KiB/MiB/GiB）与十进制（KB/MB/GB）两种单位，大小写不敏感。
// 不使用 Go 内部的 parseSize：那是 runtime 私有实现，跨版本不稳定。
func parseMemoryLimit(raw string) (int64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	upper := strings.ToUpper(text)
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{
		{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, unit.suffix) {
			multiplier = unit.factor
			upper = strings.TrimSpace(strings.TrimSuffix(upper, unit.suffix))
			break
		}
	}
	if upper == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(upper, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	// 溢出保护：上限不可能是天文数字，超过 1 TiB 视为配置错误。
	const maxReasonable = int64(1) << 40
	if value > maxReasonable/multiplier {
		return 0, false
	}
	return value * multiplier, true
}
