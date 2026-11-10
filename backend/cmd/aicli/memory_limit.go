package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// defaultMemoryLimitBytes 是未显式配置时使用的软上限。
//
// 依据（2026-11-08 在线 pprof）：大会话进程存活堆 152–209 MB，但 HeapSys
// 422–565 MB、Private Bytes 438–588 MB。差额不是泄漏，而是 GOGC=100 允许
// 堆在两倍存活之上继续扩张，Windows scavenger 在长空闲期又不会立刻把空闲
// span 交还。把软上限降到 384 MiB，配合下面的默认 GOGC=50，既保留装载
// 峰值的头部空间，又给堆高水位一个明确边界。
//
// 之所以是"软"上限：Go 的 GOMEMLIMIT 不会因超限而终止进程，只会提高 GC
// 频率并让 scavenger 更积极地归还内存，因此设得偏低最坏只是多几次 GC。
const defaultMemoryLimitBytes int64 = 384 << 20 // 384 MiB

// defaultGCPercent 是未显式配置 GOGC 时的默认回收频率。
//
// 长驻 chat 进程的存活堆以 finalized transcript / render cache / SQLite
// projection 为主，GC 本身很便宜（在线 pprof 的 GCCPUFraction 仅约 0.02%），
// 但 GOGC=100 会让 NextGC 落在存活堆的两倍，进而把 HeapSys 和进程私有内存
// 一起抬高。50 把目标压到 1.5 倍；用户仍可用 GOGC 或 AICLI_GOGC 覆盖。
const defaultGCPercent = 50

// memoryLimitEnvVars 是用户可用来覆盖软上限的环境变量，按优先级排列。
//
// 同时接受 GOMEMLIMIT（Go 官方变量）与 AICLI_MEMORY_LIMIT（本项目专用，
// 支持 "512MiB" / "512MB" / 纯字节数三种写法）。官方变量由 runtime 自己解析，
// 这里读取它只是为了"用户已设置则不再覆盖"。
var memoryLimitEnvVars = []string{"AICLI_MEMORY_LIMIT", "GOMEMLIMIT"}

// gcPercentEnvVars 是用户可用来覆盖默认 GC 频率的环境变量，按优先级排列。
// GOGC 由 Go runtime 自己解析；这里只负责"用户已设置则不再覆盖"。
var gcPercentEnvVars = []string{"AICLI_GOGC", "GOGC"}

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

// applyGCPercentFromEnv 设置运行时 GC 频率。
//
// 语义与 applyMemoryLimitFromEnv 保持一致：
//   - 用户显式配置了 AICLI_GOGC → 使用该值（支持 "50" / "off" / "none"）；
//   - 用户显式配置了 GOGC → 尊重 Go runtime 已经解析的值，不再覆盖；
//   - 都未配置 → 使用 defaultGCPercent。
//
// 返回实际生效的 GC 百分比；0 表示未修改（交给 runtime），-1 表示关闭 GC。
func applyGCPercentFromEnv() int {
	for _, name := range gcPercentEnvVars {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		if name == "GOGC" {
			// Go runtime 在进程启动时已经应用 GOGC；不要再调用
			// SetGCPercent 覆盖用户值。
			return 0
		}
		if disabledMemoryLimit(raw) {
			debug.SetGCPercent(-1)
			return -1
		}
		if value, ok := parseGCPercent(raw); ok {
			debug.SetGCPercent(value)
			return value
		}
		// 无法解析：保持 runtime 现状，不静默替换成默认值。
		return 0
	}
	debug.SetGCPercent(defaultGCPercent)
	return defaultGCPercent
}

// parseGCPercent 解析正整数 GC 百分比；0 与负数由 disabledMemoryLimit
// 统一作为"关闭"处理（与 GOGC=off 的语义一致）。
func parseGCPercent(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	// 防御性上限：超过 1000 的百分比没有实际意义，视为配置错误。
	if value > 1000 {
		return 0, false
	}
	return value, true
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
