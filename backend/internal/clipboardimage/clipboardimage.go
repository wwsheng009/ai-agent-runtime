// Package clipboardimage 从系统剪贴板读取位图并落盘为临时 PNG，
// 供 chat 的图片附件通道（/attach 与 keymap 动作）复用。
//
// 设计约束：
//   - 不引入 CGO：Windows 走 user32/kernel32 系统调用，macOS / Linux 走系统自带命令；
//   - 读不到图片时返回 ErrNoImage（而不是报「文件不存在」这类误导性错误），
//     平台/依赖缺失返回 ErrUnsupported，两者由调用方翻译成人话；
//   - 落盘文件是临时 PNG，调用方在不需要时删除，系统临时目录清理兜底。
package clipboardimage

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoImage 表示剪贴板里没有可用的位图。
var ErrNoImage = errors.New("剪贴板中没有图片")

// ErrUnsupported 表示当前平台（或缺少依赖工具）无法读取剪贴板图片。
var ErrUnsupported = errors.New("当前平台不支持读取剪贴板图片")

// Result 是一次剪贴板图片读取的结果。
type Result struct {
	Path   string // 落盘的临时 PNG 路径
	Width  int
	Height int
	Source string // 读取来源（用于诊断与提示）
}

// Read 读取剪贴板图片并写入 dir 下的临时 PNG；dir 为空时使用系统临时目录。
func Read(ctx context.Context, dir string) (Result, error) {
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return readPlatform(ctx, dir)
}

// Availability 说明当前平台是否支持读取剪贴板图片，以及原因；
// 供 /hotkeys 的终端能力矩阵如实展示。
func Availability() (bool, string) { return availability() }

// IsNoImage 判断错误是否为「剪贴板里没有图片」。
func IsNoImage(err error) bool { return errors.Is(err, ErrNoImage) }

// IsUnsupported 判断错误是否为「平台/依赖不支持」。
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }

func writePNG(dir string, img image.Image, source string) (Result, error) {
	if img == nil {
		return Result{}, ErrNoImage
	}
	file, err := os.CreateTemp(dir, "aicli-clipboard-*.png")
	if err != nil {
		return Result{}, fmt.Errorf("创建剪贴板临时文件失败: %w", err)
	}
	path := file.Name()
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Result{}, fmt.Errorf("写入剪贴板图片失败: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return Result{}, fmt.Errorf("关闭剪贴板图片文件失败: %w", err)
	}
	bounds := img.Bounds()
	return Result{
		Path:   filepath.Clean(path),
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
		Source: source,
	}, nil
}

// readPNGDimensions 只读取 PNG 头，用于外部命令落盘后的尺寸校验。
func readPNGDimensions(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
}
