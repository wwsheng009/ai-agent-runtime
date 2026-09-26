// Package imageattach 把「一张本地图片」或「一段上传字节」变成可随消息发送的附件：
// 先判体积与真实格式，再复用 imageprep 的压缩与长边/体积上限，最后落到 artifact 目录。
//
// 为什么单独一层：CLI、micro web client（`/web/api/attachments`）与 runtime server
// （`POST /api/runtime/uploads`，React frontend 用）三端只在「字节从哪来」和「路径怎么进
// 请求体」上不同——落盘、格式判定、压缩、内容哈希命名与提示文案都收敛在这里，
// 避免各端各写一套导致上限与文案漂移。
//
// 与上游的关系：路径进入请求体后仍走 internal/llm
// （ValidateLocalInputImagePaths → NewUserPromptMessageWithImages）这一既有核心，
// 本包不负责多模态编码。幂等性由 imageprep 的内容哈希命名保证：同一张图重复上传/重复
// 发送只会得到同一个 artifact 路径。
package imageattach

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"

	// 与 imageprep 的解码面保持一致（GIF 只取第一帧）；webp 不在支持面内，
	// SaveUploaded 会以「不是可识别的图片」明确拒绝。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// Limits 是发送前一并生效的上限；零值即 DefaultLimits()。
type Limits struct {
	// MaxDimension 是长边上限：0 用默认值，负数表示不缩放（关闭压缩）。
	MaxDimension int
	// MaxBytes 是单文件体积上限：<=0 用默认值。
	MaxBytes int64
}

// DefaultLimits 返回与 imageprep 一致的内置上限（1568px / 32MB）。
func DefaultLimits() Limits {
	return Limits{MaxDimension: imageprep.DefaultMaxDimension, MaxBytes: imageprep.DefaultMaxBytes}
}

func (l Limits) options() imageprep.Options {
	if l.MaxBytes <= 0 {
		l.MaxBytes = imageprep.DefaultMaxBytes
	}
	return imageprep.Options{MaxDimension: l.MaxDimension, MaxBytes: l.MaxBytes}
}

// Prepared 是一张图片的处理结果；Skipped 时 Path 为空、Note 说明原因。
type Prepared struct {
	Path    string
	Note    string
	Skipped bool
	Bytes   int64
	Width   int
	Height  int
}

// PrepareLocal 处理磁盘上已有的图片路径（压缩 + 长边/体积上限）。
// 已在上限内的图片原样返回（Path == srcPath），不重新编码。
func PrepareLocal(srcPath, artifactDir string, limits Limits) (Prepared, error) {
	result, err := imageprep.Prepare(srcPath, artifactDir, limits.options())
	if err != nil {
		return Prepared{}, err
	}
	return fromResult(result), nil
}

// PrepareLocalAll 按输入顺序批量处理。单项失败不中断整批：失败项以
// Skipped + Note 呈现，交由调用方决定是提示还是回退（与 CLI 的"按规则跳过的
// 不阻塞其它图片"一致）。空输入返回 nil。
func PrepareLocalAll(paths []string, artifactDir string, limits Limits) []Prepared {
	if len(paths) == 0 {
		return nil
	}
	out := make([]Prepared, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		prepared, err := PrepareLocal(trimmed, artifactDir, limits)
		if err != nil {
			out = append(out, Prepared{
				Note:    fmt.Sprintf("已跳过 %s：%s", trimmed, err.Error()),
				Skipped: true,
			})
			continue
		}
		out = append(out, prepared)
	}
	return out
}

// SaveUploaded 把上传的字节变成可发送附件：
//  1. 先按体积上限拒收（不把超大文件写进磁盘，也不让它进请求体）；
//  2. 用解码器判**真实**格式（不信扩展名），不是图片就报错；
//  3. 落临时文件后交给 imageprep（内容哈希命名，天然去重并幂等），最后清理临时文件。
//
// filename 只用于提示文案与临时文件后缀，不参与安全判断。
func SaveUploaded(data []byte, filename, artifactDir string, limits Limits) (Prepared, error) {
	opts := limits.options()
	if len(data) == 0 {
		return Prepared{}, fmt.Errorf("上传内容为空")
	}
	if int64(len(data)) > opts.MaxBytes {
		return Prepared{
			Bytes:   int64(len(data)),
			Skipped: true,
			Note: fmt.Sprintf("已跳过 %s：%s 超过 %s 上限（图片不会随消息发送）",
				displayName(filename), humanBytes(int64(len(data))), humanBytes(opts.MaxBytes)),
		}, nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Prepared{}, fmt.Errorf("%s 不是可识别的图片（%s / PNG / JPEG / GIF）", displayName(filename), err.Error())
	}
	if config.Width <= 0 || config.Height <= 0 {
		return Prepared{}, fmt.Errorf("%s 的图片尺寸无效（%dx%d）", displayName(filename), config.Width, config.Height)
	}

	// 落盘名取**内容哈希**：同一张图重复上传命中同一个文件（幂等），不同内容不会
	// 互相覆盖；原始文件名只用于提示文案。先在 artifact 目录内写临时文件再 rename，
	// 避免并发读看到半截文件（Windows 上 os.Rename 会替换已存在的目标）。
	//
	// 关键：上传必须产出**持久**路径。imageprep 对已在上限内的图片返回原样路径，
	// 所以这里不能把字节先写到会被清理的临时目录——否则"无需压缩"的上传会返回
	// 一个随即失效的路径（回归见 imageattach_test.go）。
	baseDir := strings.TrimSpace(artifactDir)
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return Prepared{}, fmt.Errorf("创建附件目录失败: %w", err)
	}
	sum := sha1.Sum(data)
	uploadPath := filepath.Join(baseDir, fmt.Sprintf("upload-%s.%s", hex.EncodeToString(sum[:4]), format))
	if _, statErr := os.Stat(uploadPath); statErr != nil {
		staged, err := os.CreateTemp(baseDir, ".upload-*.tmp")
		if err != nil {
			return Prepared{}, fmt.Errorf("写入上传内容失败: %w", err)
		}
		stagedPath := staged.Name()
		if _, err := staged.Write(data); err != nil {
			staged.Close()
			os.Remove(stagedPath)
			return Prepared{}, fmt.Errorf("写入上传内容失败: %w", err)
		}
		if err := staged.Close(); err != nil {
			os.Remove(stagedPath)
			return Prepared{}, fmt.Errorf("写入上传内容失败: %w", err)
		}
		if err := os.Rename(stagedPath, uploadPath); err != nil {
			os.Remove(stagedPath)
			return Prepared{}, fmt.Errorf("落盘上传内容失败: %w", err)
		}
	}

	prepared, err := PrepareLocal(uploadPath, artifactDir, limits)
	if err != nil {
		os.Remove(uploadPath)
		return Prepared{}, err
	}
	if prepared.Path != uploadPath {
		// 需要缩放/转码时 imageprep 另写了产物，原始上传副本没有保留价值。
		os.Remove(uploadPath)
	}
	return prepared, nil
}

func fromResult(result imageprep.Result) Prepared {
	return Prepared{
		Path:    result.Path,
		Note:    result.Note,
		Skipped: result.Skipped,
		Bytes:   result.Bytes,
		Width:   result.Width,
		Height:  result.Height,
	}
}

// displayName 取上传文件名用于提示；缺失时给一个中性称呼。
func displayName(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		return "上传的图片"
	}
	return filepath.Base(name)
}

// humanBytes 与 imageprep 的提示口径一致（一位小数的 MB，其余按 KB / B）。
func humanBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", size)
	}
}
