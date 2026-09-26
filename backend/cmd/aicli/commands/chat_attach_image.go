package commands

import (
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"
)

// 发送前图片处理：长边默认上限与单文件体积上限。
const (
	defaultChatImageMaxDimension = imageprep.DefaultMaxDimension
	maxChatImageBytes            = imageprep.DefaultMaxBytes
)

// envChatImageMaxDimension 可覆盖长边上限：0 表示关闭压缩，负数表示只做体积上限检查。
const envChatImageMaxDimension = "AICLI_IMAGE_MAX_DIMENSION"

// preparedChatImage 是一次发送前处理的结果。
type preparedChatImage struct {
	// Path 是处理后的路径；为空表示该图片应被跳过（Note 说明原因）。
	Path string
	// Note 是一行可直接展示的说明；无需处理时为空。
	Note string
}

// prepareChatImageAttachment 在图片进入待发送附件前做统一处理：超过长边上限的
// 等比缩小（含透明通道保 PNG，其余转 JPEG），超过体积上限的直接跳过并给出原因。
// 处理后的文件落在会话 artifact 目录（无会话目录时退回系统临时目录）。
func prepareChatImageAttachment(session *ChatSession, path string) (preparedChatImage, error) {
	result, err := imageprep.Prepare(path, chatImageArtifactDir(session), imageprep.Options{
		MaxDimension: chatImageMaxDimension(),
		MaxBytes:     maxChatImageBytes,
	})
	if err != nil {
		return preparedChatImage{}, err
	}
	if result.Skipped {
		return preparedChatImage{Note: result.Note}, nil
	}
	return preparedChatImage{Path: result.Path, Note: result.Note}, nil
}

// chatImageMaxDimension 读取长边上限；未配置或配置非法时用默认值。
// 约定：环境变量 0 表示"关闭压缩"，在 imageprep 的语义里对应负值（不缩放）；
// 负值同样表示不缩放。这样"关闭"不会被误读成"使用默认上限"。
func chatImageMaxDimension() int {
	raw := strings.TrimSpace(os.Getenv(envChatImageMaxDimension))
	if raw == "" {
		return defaultChatImageMaxDimension
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultChatImageMaxDimension
	}
	if value == 0 {
		return -1
	}
	return value
}

func chatImageArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return chatSessionImageArtifactDir(session)
}
