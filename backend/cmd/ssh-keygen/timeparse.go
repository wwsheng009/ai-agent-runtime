package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 本文件实现证书有效期解析，语义与 OpenSSH ssh-keygen.c 的 parse_cert_times /
// parse_absolute_time / parse_relative_time / convtime 完全对齐。

// parseCertTimes 解析 -V 参数。
// 与 OpenSSH 一致，返回 (validAfter, validBefore)。
func parseCertTimes(spec string, now time.Time) (uint64, uint64, error) {
	// +timespec：相对 now，from 回拨 1 分钟（对齐时钟偏差）
	if strings.HasPrefix(spec, "+") && !strings.Contains(spec, ":") {
		secs, err := convtime(spec[1:])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid relative certificate life %q: %v", spec, err)
		}
		to := now.Unix() + secs
		from := ((now.Unix() - 59) / 60) * 60 // 对齐到分钟并回拨
		if uint64(to) <= uint64(from) {
			return 0, 0, fmt.Errorf("empty certificate validity interval")
		}
		return uint64(from), uint64(to), nil
	}

	// from:to
	idx := strings.Index(spec, ":")
	if idx <= 0 || idx == len(spec)-1 {
		return 0, 0, fmt.Errorf("invalid certificate life specification %q", spec)
	}
	from, to := spec[:idx], spec[idx+1:]

	validFrom, err := parseCertTimeBound(from, now, true)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid from time %q: %v", from, err)
	}
	validTo, err := parseCertTimeBound(to, now, false)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid to time %q: %v", to, err)
	}
	if validTo <= validFrom {
		return 0, 0, fmt.Errorf("empty certificate validity interval")
	}
	return validFrom, validTo, nil
}

// parseCertTimeBound 解析 from:to 的单个边界。
// isFrom 为 true 时支持 "always"（0）；否则支持 "forever"（uint64 max）。
func parseCertTimeBound(s string, now time.Time, isFrom bool) (uint64, error) {
	switch {
	case s == "always":
		if !isFrom {
			return 0, fmt.Errorf("%q is only valid as a from time", s)
		}
		return 0, nil
	case s == "forever":
		if isFrom {
			return 0, fmt.Errorf("%q is only valid as a to time", s)
		}
		return CertTimeInfinity, nil
	case strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-"):
		secs, err := convtime(s[1:])
		if err != nil {
			return 0, err
		}
		if strings.HasPrefix(s, "-") {
			if int64(secs) > now.Unix() {
				return 0, fmt.Errorf("certificate time %q cannot be represented", s)
			}
			return uint64(now.Unix() - secs), nil
		}
		return uint64(now.Unix() + secs), nil
	case strings.HasPrefix(s, "0x"):
		n, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid hex time %q", s)
		}
		return n, nil
	default:
		return parseAbsoluteTime(s)
	}
}

// convtime 解析相对时长（OpenSSH misc.c convtime_double 的整数版本）。
// 支持后缀 s/m/h/d/w（秒/分/时/天/周），可组合如 "1h30m"、"2w3d"。
// 纯数字视为秒。重复的秒单位报错（与 OpenSSH 一致）。
func convtime(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty time string")
	}
	var total int64
	i := 0
	seenSeconds := false
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if start == i {
			return 0, fmt.Errorf("invalid character %q at offset %d", s[i], i)
		}
		n, err := strconv.ParseInt(s[start:i], 10, 64)
		if err != nil {
			return 0, err
		}
		var mult int64
		isSeconds := false
		switch {
		case i >= len(s):
			mult = 1
			isSeconds = true
		case s[i] == 's' || s[i] == 'S':
			mult = 1
			isSeconds = true
			i++
		case s[i] == 'm' || s[i] == 'M':
			mult = 60
			i++
		case s[i] == 'h' || s[i] == 'H':
			mult = 3600
			i++
		case s[i] == 'd' || s[i] == 'D':
			mult = 86400
			i++
		case s[i] == 'w' || s[i] == 'W':
			mult = 604800
			i++
		default:
			return 0, fmt.Errorf("invalid unit %q", s[i])
		}
		if isSeconds && seenSeconds {
			return 0, fmt.Errorf("duplicate seconds unit")
		}
		if isSeconds {
			seenSeconds = true
		}
		total += n * mult
	}
	return total, nil
}

// parseAbsoluteTime 解析 YYYYMMDD / YYYYMMDDHHMM / YYYYMMDDHHMMSS，
// 支持可选后缀 Z / UTC（UTC 时间，默认本地时区）。
// 与 OpenSSH misc.c parse_absolute_time 对齐。
func parseAbsoluteTime(s string) (uint64, error) {
	orig := s
	isUTC := false
	if strings.HasSuffix(strings.ToUpper(s), "Z") && len(s) > 1 {
		isUTC = true
		s = s[:len(s)-1]
	} else if strings.HasSuffix(strings.ToUpper(s), "UTC") && len(s) > 3 {
		isUTC = true
		s = s[:len(s)-3]
	}

	var layout string
	switch len(s) {
	case 8: // YYYYMMDD
		layout = "20060102"
	case 12: // YYYYMMDDHHMM
		layout = "200601021504"
	case 14: // YYYYMMDDHHMMSS
		layout = "20060102150405"
	default:
		return 0, fmt.Errorf("invalid absolute time %q (want YYYYMMDD[HHMM[SS]][Z|UTC])", orig)
	}

	loc := time.Local
	if isUTC {
		loc = time.UTC
	}
	t, err := time.ParseInLocation(layout, s, loc)
	if err != nil {
		return 0, fmt.Errorf("invalid absolute time %q: %v", orig, err)
	}
	return uint64(t.Unix()), nil
}

// formatAbsoluteTime 以 OpenSSH 风格格式化证书时间（本地时区）。
func formatAbsoluteTime(t uint64) string {
	if t == 0 {
		return "past"
	}
	if t == 1 {
		return "now"
	}
	if t >= CertTimeInfinity {
		return "forever"
	}
	return time.Unix(int64(t), 0).Format("2006-01-02T15:04:05")
}

// formatValidity 输出证书有效期的可读描述（ssh-keygen -L 的 Valid 行）。
func formatValidity(from, to uint64) string {
	return fmt.Sprintf("from %s to %s", formatAbsoluteTime(from), formatAbsoluteTime(to))
}
