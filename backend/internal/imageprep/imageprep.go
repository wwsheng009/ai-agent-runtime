// Package imageprep 负责「发送前」的本地图片处理：把过大的图片缩到长边上限、
// 按是否含透明通道选择 PNG/JPEG 重新编码，并拒绝超过体积上限的文件。
//
// 规则（与主流视觉模型的现实约束对齐）：
//   - 只缩不放：长边已在上限内的图片原样返回，不重新编码，避免无谓的质量损失；
//   - 含透明通道的图保持 PNG，其余转 JPEG；
//   - 超过体积上限的图标记 Skipped 并给出原因，由调用方提示用户；不静默发送原图。
//
// 长边上限是硬规则：扁平/合成类图片（例如 UI 截图）用 PNG 压得极小，重新编码成
// JPEG 反而会变大，但它的视觉 token 开销仍由像素数决定，所以照样要缩——Note 会
// 如实写出体积变化，绝不用"省字节"的名义绕过尺寸上限。
package imageprep

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	// 注册 GIF 解码器；动画只取第一帧。
	_ "image/gif"
)

const (
	// DefaultMaxDimension 是长边默认上限。多数视觉模型会把输入缩到 ~1568px，
	// 再大只会额外消耗 token 与带宽。
	DefaultMaxDimension = 1568
	// DefaultMaxBytes 是单个文件的体积上限。
	DefaultMaxBytes = 32 << 20
	// DefaultJPEGQuality 是无透明通道图片的重新编码质量。
	DefaultJPEGQuality = 85
)

// Options 控制一次预处理；零值即默认策略。
type Options struct {
	// MaxDimension 是长边上限：0 用 DefaultMaxDimension，负数表示不缩放。
	MaxDimension int
	// MaxBytes 是体积上限：<=0 用 DefaultMaxBytes。
	MaxBytes int64
	// JPEGQuality 是 JPEG 质量：<=0 用 DefaultJPEGQuality。
	JPEGQuality int
}

// Result 描述一次预处理的结果。
type Result struct {
	// Path 是处理后的路径；Skipped 时为空。
	Path string
	// Width/Height 是处理后的像素尺寸（未处理时为原尺寸）。
	Width  int
	Height int
	// Bytes 是处理后（或原图）的文件体积。
	Bytes int64
	// Format 是处理后的格式：png / jpeg；未读取或跳过时为空。
	Format string
	// Rewritten 表示是否写入了一个新文件。
	Rewritten bool
	// Skipped 表示因超过体积上限而应放弃该图片。
	Skipped bool
	// Note 是一行中文说明，可直接展示给用户；无需处理时为空。
	Note string
}

// Prepare 按 opts 处理 srcPath，必要时把结果写入 outDir（为空则用系统临时目录）。
// 只有真正需要缩放/转码时才写文件；返回的 Result 一定带有可供用户判断的信息。
func Prepare(srcPath, outDir string, opts Options) (Result, error) {
	srcPath = strings.TrimSpace(srcPath)
	if srcPath == "" {
		return Result{}, fmt.Errorf("图片路径为空")
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return Result{}, fmt.Errorf("读取图片信息失败: %w", err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("路径是目录而不是图片: %s", srcPath)
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if info.Size() > maxBytes {
		return Result{
			Bytes:   info.Size(),
			Skipped: true,
			Note: fmt.Sprintf("已跳过 %s：%s 超过 %s 上限（图片不会随消息发送）",
				filepath.Base(srcPath), humanBytes(info.Size()), humanBytes(maxBytes)),
		}, nil
	}

	cfg, format, err := decodeConfig(srcPath)
	if err != nil {
		return Result{}, err
	}
	format = normalizeFormat(format)

	unchanged := Result{Path: srcPath, Width: cfg.Width, Height: cfg.Height, Bytes: info.Size(), Format: format}
	maxDimension := opts.MaxDimension
	if maxDimension == 0 {
		maxDimension = DefaultMaxDimension
	}
	if maxDimension < 0 || (cfg.Width <= maxDimension && cfg.Height <= maxDimension) {
		return unchanged, nil
	}

	src, _, err := decodeFile(srcPath)
	if err != nil {
		return Result{}, err
	}
	dstWidth, dstHeight := fitWithin(cfg.Width, cfg.Height, maxDimension)
	scaled := downscale(src, dstWidth, dstHeight)
	hasAlpha := imageHasAlpha(src)

	quality := opts.JPEGQuality
	if quality <= 0 {
		quality = DefaultJPEGQuality
	}
	payload, outFormat, err := encodeImage(scaled, hasAlpha, quality)
	if err != nil {
		return Result{}, err
	}

	dstPath, err := writeResult(srcPath, outDir, dstWidth, dstHeight, outFormat, payload.Bytes())
	if err != nil {
		return Result{}, err
	}
	return Result{
		Path:      dstPath,
		Width:     dstWidth,
		Height:    dstHeight,
		Bytes:     int64(payload.Len()),
		Format:    outFormat,
		Rewritten: true,
		Note: fmt.Sprintf("已压缩 %dx%d → %dx%d（%s → %s，%s）",
			cfg.Width, cfg.Height, dstWidth, dstHeight,
			humanBytes(info.Size()), humanBytes(int64(payload.Len())), strings.ToUpper(outFormat)),
	}, nil
}

func decodeConfig(path string) (image.Config, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return image.Config{}, "", fmt.Errorf("打开图片失败: %w", err)
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		return image.Config{}, "", fmt.Errorf("无法识别的图片格式: %w", err)
	}
	return cfg, format, nil
}

func decodeFile(path string) (image.Image, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("打开图片失败: %w", err)
	}
	defer file.Close()
	img, format, err := image.Decode(file)
	if err != nil {
		return nil, "", fmt.Errorf("解码图片失败: %w", err)
	}
	return img, format, nil
}

// encodeImage 编码缩放结果：含透明通道用 PNG，否则用 JPEG。
func encodeImage(img image.Image, hasAlpha bool, quality int) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	if hasAlpha {
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("编码 PNG 失败: %w", err)
		}
		return &buf, "png", nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", fmt.Errorf("编码 JPEG 失败: %w", err)
	}
	return &buf, "jpeg", nil
}

func writeResult(srcPath, outDir string, width, height int, format string, payload []byte) (string, error) {
	dir := strings.TrimSpace(outDir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建图片输出目录失败: %w", err)
	}
	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	// 文件名带内容哈希：同一张图（哪怕来自不同的临时文件）会落到同一个路径，
	// 上层按路径去重即可天然幂等。
	name := fmt.Sprintf("%s-%dx%d-%s%s", sanitizeBaseName(filepath.Base(srcPath)), width, height, shortHash(payload), ext)
	dstPath := filepath.Join(dir, name)
	tmpPath := dstPath + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("保存图片失败: %w", err)
	}
	return dstPath, nil
}

// fitWithin 按长边上限等比缩小；不放大，最小边至少 1 像素。
func fitWithin(width, height, maxDimension int) (int, int) {
	if width <= 0 || height <= 0 || maxDimension <= 0 {
		return width, height
	}
	if width <= maxDimension && height <= maxDimension {
		return width, height
	}
	if width >= height {
		scaled := height * maxDimension / width
		if scaled < 1 {
			scaled = 1
		}
		return maxDimension, scaled
	}
	scaled := width * maxDimension / height
	if scaled < 1 {
		scaled = 1
	}
	return scaled, maxDimension
}

// downscale 用面积平均（box filter）缩放：每个目标像素取源图对应矩形的平均颜色。
// 相比最近邻能避免缩略图里的锯齿与摩尔纹，且对整数/非整数比例都成立。
func downscale(src image.Image, dstWidth, dstHeight int) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	for dy := 0; dy < dstHeight; dy++ {
		y0 := bounds.Min.Y + dy*bounds.Dy()/dstHeight
		y1 := bounds.Min.Y + (dy+1)*bounds.Dy()/dstHeight
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dstWidth; dx++ {
			x0 := bounds.Min.X + dx*bounds.Dx()/dstWidth
			x1 := bounds.Min.X + (dx+1)*bounds.Dx()/dstWidth
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sumR, sumG, sumB, sumA, count uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					pixel := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
					sumR += uint64(pixel.R)
					sumG += uint64(pixel.G)
					sumB += uint64(pixel.B)
					sumA += uint64(pixel.A)
					count++
				}
			}
			if count == 0 {
				continue
			}
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: uint8(sumR / count),
				G: uint8(sumG / count),
				B: uint8(sumB / count),
				A: uint8(sumA / count),
			})
		}
	}
	return dst
}

// imageHasAlpha 判断源图是否存在半透明/全透明像素（决定编码格式）。
func imageHasAlpha(img image.Image) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha < 0xffff {
				return true
			}
		}
	}
	return false
}

func normalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "jpeg"
	case "png":
		return "png"
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

func sanitizeBaseName(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := strings.Trim(builder.String(), "-")
	if out == "" {
		out = "image"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func shortHash(payload []byte) string {
	sum := sha1.Sum(payload)
	return hex.EncodeToString(sum[:])[:8]
}

// humanBytes 输出便于阅读的体积文本（1 位小数）。
func humanBytes(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(size)/float64(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", size)
	}
}
