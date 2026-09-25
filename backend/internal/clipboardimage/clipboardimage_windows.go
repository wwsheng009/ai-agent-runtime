//go:build windows

package clipboardimage

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfDIB   = 8
	cfDIBV5 = 17
	// maxDIBBytes 限制单张剪贴板位图的大小（约 200MB），避免异常数据触发巨额分配。
	maxDIBBytes = 200 << 20
)

func availability() (bool, string) {
	return true, "Windows 剪贴板位图（CF_DIB/CF_DIBV5 → 临时 PNG）"
}

// readPlatform 通过 user32 直接读取 CF_DIB/CF_DIBV5。
// 剪贴板是全局独占资源，被其它程序短暂占用时 OpenClipboard 会失败，这里做有限重试。
func readPlatform(ctx context.Context, dir string) (Result, error) {
	var openErr error
	opened := false
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		ok, _, callErr := procOpenClipboard.Call(0)
		if ok != 0 {
			opened = true
			break
		}
		openErr = callErr
		time.Sleep(time.Duration(20+attempt*20) * time.Millisecond)
	}
	if !opened {
		if openErr == nil {
			openErr = fmt.Errorf("剪贴板被其它程序占用")
		}
		return Result{}, fmt.Errorf("打开剪贴板失败（可能被其它程序占用）: %w", openErr)
	}
	defer procCloseClipboard.Call()

	for _, format := range []uintptr{cfDIBV5, cfDIB} {
		available, _, _ := procIsClipboardFormatAvailable.Call(format)
		if available == 0 {
			continue
		}
		handle, _, _ := procGetClipboardData.Call(format)
		if handle == 0 {
			continue
		}
		pointer, _, _ := procGlobalLock.Call(handle)
		if pointer == 0 {
			continue
		}
		size, _, _ := procGlobalSize.Call(handle)
		if size == 0 || size > maxDIBBytes {
			procGlobalUnlock.Call(handle)
			continue
		}
		// 用 RtlMoveMemory 复制一份再解码：既避免解锁后剪贴板内容被其它进程改动，
		// 也避免 uintptr → unsafe.Pointer 的转换（vet 的 unsafeptr 检查）。
		data := make([]byte, int(size))
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&data[0])), pointer, size)
		procGlobalUnlock.Call(handle)

		decoded, err := decodeDIB(data)
		if err != nil {
			continue
		}
		return writePNG(dir, decoded, "windows-clipboard")
	}
	return Result{}, ErrNoImage
}
