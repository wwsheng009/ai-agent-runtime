package tools

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// viewSupportedImageFormats 与 imageprep 的解码面保持一致（png/jpeg/gif）：
// 只有能真实解码的格式才走图片直通。
var viewSupportedImageFormats = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
}

// viewDetectedOnlyImageFormats 能可靠嗅探、但当前链路不解码的格式：返回一行
// 真实 MIME + 恢复路径，而不是"疑似二进制文件"的模糊报错。
var viewDetectedOnlyImageFormats = map[string]string{
	"webp": "image/webp",
	"bmp":  "image/bmp",
	"tiff": "image/tiff",
}

// sniffImageFormat 按 magic bytes 判定真实格式；扩展名一律不可信
// （analysis §3.11：假 .png 必须走真实格式判定，错扩展名的真图片也必须能用）。
func sniffImageFormat(head []byte) (string, bool) {
	switch {
	case len(head) >= 8 && bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png", true
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return "jpeg", true
	case len(head) >= 6 && (bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a"))):
		return "gif", true
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp", true
	case looksLikeBMP(head):
		return "bmp", true
	case len(head) >= 4 && (bytes.HasPrefix(head, []byte{0x49, 0x49, 0x2A, 0x00}) || bytes.HasPrefix(head, []byte{0x4D, 0x4D, 0x00, 0x2A})):
		return "tiff", true
	}
	return "", false
}

// looksLikeBMP 不接受裸 "BM" 两字节：文本文件以 "BM" 开头（例如 "BM25
// ranking"）会被误判成图片并卡住 view。BMP 头部还要求保留字段为 0 且 DIB
// 头长度是已知值（12/40/52/56/64/108/124）。
func looksLikeBMP(head []byte) bool {
	if len(head) < 18 || head[0] != 'B' || head[1] != 'M' {
		return false
	}
	for _, reserved := range head[6:10] {
		if reserved != 0 {
			return false
		}
	}
	dibSize := int(head[14]) | int(head[15])<<8 | int(head[16])<<16 | int(head[17])<<24
	switch dibSize {
	case 12, 40, 52, 56, 64, 108, 124:
		return true
	}
	return false
}

func readViewFileHead(path string, size int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	head := make([]byte, size)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return head[:n], nil
}

// viewImageResult 让 view 对图片返回可发送的图片附件（而非二进制报错）：
// 先按 magic bytes 嗅探真实格式，再用 imageprep 校验并复用其长边/体积上限与缩放说明。
// 第二个返回值是 false 时调用方按普通文本读取路径继续（非图片、超限图等）。
func (v *ViewTool) viewImageResult(absPath, displayPath string) (*toolkit.ToolResult, bool) {
	head, err := readViewFileHead(absPath, 64)
	if err != nil {
		return nil, false
	}
	format, isImage := sniffImageFormat(head)
	if !isImage {
		return nil, false
	}
	var size int64
	if info, statErr := os.Stat(absPath); statErr == nil {
		size = info.Size()
	}

	mimeType, supported := viewSupportedImageFormats[format]
	if !supported {
		detectedMIME := viewDetectedOnlyImageFormats[format]
		if detectedMIME == "" {
			detectedMIME = "application/octet-stream"
		}
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"图片文件: %s\n真实格式: %s（magic bytes 判定，扩展名不可信），大小: %d 字节。\n"+
					"当前图片链路支持 png/jpeg/gif，未附加图像输入；可用 download/shell 转换为 PNG 后重试 view。",
				displayPath, detectedMIME, size,
			),
			Metadata: map[string]interface{}{
				"file_path":             absPath,
				"image_passthrough":     false,
				"image_skipped":         true,
				"image_detected_format": format,
				"image_mime_type":       detectedMIME,
				"image_bytes":           size,
				"image_skip_reason":     "unsupported_image_format",
			},
		}, true
	}

	prepared, err := imageprep.Prepare(absPath, "", imageprep.Options{})
	if err != nil {
		return nil, false
	}
	if prepared.Skipped {
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"图片文件: %s\n%s\n（图片超出体积上限，未附加图像输入；可先用 shell/download 压缩后重试）",
				displayPath, prepared.Note,
			),
			Metadata: map[string]interface{}{
				"file_path":             absPath,
				"image_skipped":         true,
				"image_mime_type":       mimeType,
				"image_detected_format": format,
				"image_bytes":           prepared.Bytes,
				"image_passthrough":     false,
			},
		}, true
	}
	preparedPath := strings.TrimSpace(prepared.Path)
	if preparedPath == "" {
		return nil, false
	}
	// 缩放产物默认落在系统临时目录，可能与清理竞争；改为内容寻址的稳定目录，
	// 保证下一步 provider 请求（以及历史重放）仍能读到同一张图。
	if prepared.Rewritten {
		if stablePath, persistErr := persistViewImage(preparedPath); persistErr == nil {
			preparedPath = stablePath
		}
	}

	switch strings.ToLower(strings.TrimSpace(prepared.Format)) {
	case "png":
		mimeType = "image/png"
	case "jpeg", "jpg":
		mimeType = "image/jpeg"
	case "gif":
		mimeType = "image/gif"
	}

	metadata := map[string]interface{}{
		"file_path":                            absPath,
		toolresult.MetadataImagePassthroughKey: true,
		toolresult.MetadataImagePathKey:        preparedPath,
		toolresult.MetadataImageMimeTypeKey:    mimeType,
		toolresult.MetadataImageWidthKey:       prepared.Width,
		toolresult.MetadataImageHeightKey:      prepared.Height,
		toolresult.MetadataImageBytesKey:       prepared.Bytes,
		"image_detected_format":                format,
	}
	content := fmt.Sprintf(
		"图片文件: %s\nMIME: %s，尺寸: %dx%d，大小: %d 字节\n已作为图片输入附加到下一轮对话。",
		displayPath, mimeType, prepared.Width, prepared.Height, prepared.Bytes,
	)
	if note := strings.TrimSpace(prepared.Note); note != "" {
		metadata[toolresult.MetadataImageNoteKey] = note
		content += "\n" + note
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindStructured,
		Content:    content,
		Metadata:   metadata,
	}, true
}

// persistViewImage 把缩放/转码产物复制到内容寻址目录，同一张图幂等复用。
func persistViewImage(srcPath string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), "ai-agent-runtime-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fileBytesSHA256(data)[:16] + strings.ToLower(filepath.Ext(srcPath))
	dst := filepath.Join(dir, name)
	if _, statErr := os.Stat(dst); statErr == nil {
		return dst, nil
	}
	if err := writeFileAtomic(dst, data, writeFileModeDefault); err != nil {
		return "", err
	}
	return dst, nil
}
