package ui

import (
	"errors"
	"os"
	"testing"
)

// P1-2b 几何收敛：driver.ProbeSize 是唯一 raw 探测点；Size() 只读缓存；
// GetTerminalWidth/Height/Size 转发进程级缓存，展示宽度调用点不再逐次探测。
func TestTerminalDriverProbeSizeIsSingleProbeAndPublishesCache(t *testing.T) {
	resetProcessTerminalSizeCacheForTest()
	originalProbe := driverSizeProbe
	probes := 0
	driverSizeProbe = func(fd int) (int, int, error) {
		probes++
		return 111, 33, nil
	}
	t.Cleanup(func() {
		driverSizeProbe = originalProbe
		resetProcessTerminalSizeCacheForTest()
	})

	driver := &TerminalDriver{stdout: os.Stdout}
	if width, height, err := driver.ProbeSize(); err != nil || width != 111 || height != 33 {
		t.Fatalf("ProbeSize = (%d,%d,%v), want (111,33,nil)", width, height, err)
	}
	if probes != 1 {
		t.Fatalf("ProbeSize issued %d raw probes, want 1", probes)
	}

	// 展示宽度调用点走缓存转发，不得触发第二次探测。
	if width, height := GetTerminalWidth(), GetTerminalHeight(); width != 111 || height != 33 {
		t.Fatalf("display readers = (%d,%d), want (111,33)", width, height)
	}
	if width, height := GetTerminalSize(); width != 111 || height != 33 {
		t.Fatalf("GetTerminalSize = (%d,%d), want (111,33)", width, height)
	}
	if probes != 1 {
		t.Fatalf("cache-forwarded readers issued %d raw probes, want 1", probes)
	}

	// Size() 只读能力缓存，不再 syscall。
	if width, height, err := driver.Size(); err != nil || width != 111 || height != 33 {
		t.Fatalf("Size = (%d,%d,%v), want (111,33,nil)", width, height, err)
	}
	if probes != 1 {
		t.Fatalf("Size issued %d raw probes, want 1", probes)
	}

	// Terminal.RefreshSize 经 ProbeSize 刷新一次并转发结果。
	terminal := &Terminal{driver: driver}
	if width, height := terminal.RefreshSize(); width != 111 || height != 33 {
		t.Fatalf("RefreshSize = (%d,%d), want (111,33)", width, height)
	}
	if probes != 2 {
		t.Fatalf("RefreshSize probe count = %d, want 2", probes)
	}
}

// 探测失败不得污染缓存，也不得改写能力缓存。
func TestTerminalDriverProbeSizeFailureLeavesCacheUntouched(t *testing.T) {
	resetProcessTerminalSizeCacheForTest()
	originalProbe := driverSizeProbe
	driverSizeProbe = func(fd int) (int, int, error) { return 0, 0, errors.New("no tty") }
	t.Cleanup(func() {
		driverSizeProbe = originalProbe
		resetProcessTerminalSizeCacheForTest()
	})

	driver := &TerminalDriver{stdout: os.Stdout}
	if _, _, err := driver.ProbeSize(); err == nil {
		t.Fatal("ProbeSize succeeded without a tty, want error")
	}
	if _, _, ok := cachedProcessTerminalSize(); ok {
		t.Fatal("failed probe polluted the process size cache")
	}
	if width, height, err := driver.Size(); err != nil || width != 80 || height != 24 {
		t.Fatalf("Size fallback = (%d,%d,%v), want (80,24,nil)", width, height, err)
	}
}
