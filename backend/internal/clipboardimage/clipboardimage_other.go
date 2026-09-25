//go:build !windows && !darwin && !linux

package clipboardimage

import (
	"context"
	"runtime"
)

func availability() (bool, string) {
	return false, "当前平台（" + runtime.GOOS + "）暂不支持读取剪贴板图片"
}

func readPlatform(context.Context, string) (Result, error) {
	return Result{}, ErrUnsupported
}
