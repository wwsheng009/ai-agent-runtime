//go:build linux

package background

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func sampleProcessUsage(pid int) (processUsage, bool) {
	if pid <= 0 {
		return processUsage{}, false
	}
	usage := processUsage{OK: true}

	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			if pages, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				usage.RSSKB = pages * int64(os.Getpagesize()) / 1024
			}
		}
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		fields := strings.Fields(string(data))
		// utime/stime are fields 14/15 (1-based); USER_HZ is 100 on Linux.
		if len(fields) >= 15 {
			utime, _ := strconv.ParseFloat(fields[13], 64)
			stime, _ := strconv.ParseFloat(fields[14], 64)
			usage.CPUSeconds = (utime + stime) / 100.0
		}
	}
	return usage, true
}
