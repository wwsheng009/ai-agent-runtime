package searchcore

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseGrepSize parses the size spellings the grep surface accepts: a plain
// byte count, or a K/M/G suffix (1024-based) with an optional B. It is the
// single definition shared by the local tool and the remote schema.
func ParseGrepSize(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(raw))
	if value == "" {
		return 0, fmt.Errorf("不能为空")
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(value, "K"):
		multiplier = 1024
		value = strings.TrimSuffix(value, "K")
	case strings.HasSuffix(value, "M"):
		multiplier = 1024 * 1024
		value = strings.TrimSuffix(value, "M")
	case strings.HasSuffix(value, "G"):
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSuffix(value, "G")
	case strings.HasSuffix(value, "B"):
		value = strings.TrimSuffix(value, "B")
	}
	base, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析大小 %q", raw)
	}
	if base < 0 {
		return 0, fmt.Errorf("大小不能为负数")
	}
	return base * multiplier, nil
}
