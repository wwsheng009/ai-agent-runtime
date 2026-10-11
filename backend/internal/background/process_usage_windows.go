//go:build windows

package background

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modpsapi              = windows.NewLazySystemDLL("psapi.dll")
	fGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func sampleProcessUsage(pid int) (processUsage, bool) {
	if pid <= 0 {
		return processUsage{}, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processUsage{}, false
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return processUsage{}, false
	}
	usage := processUsage{
		CPUSeconds: float64(kernel.Nanoseconds()+user.Nanoseconds()) / 1e9,
		OK:         true,
	}
	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))
	if ret, _, _ := fGetProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	); ret != 0 {
		usage.RSSKB = int64(counters.WorkingSetSize) / 1024
	}
	return usage, true
}
