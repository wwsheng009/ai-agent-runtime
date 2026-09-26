package commands

import (
	"os"
	"strconv"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"
)

// 发送前图片处理：长边默认上限与单文件体积上限。
const (
	defaultChatImageMaxDimension = imageprep.DefaultMaxDimension
	maxChatImageBytes            = imageprep.DefaultMaxBytes
	chatImageMBBytes             = 1 << 20
)

// 环境变量覆盖（优先于配置文件）：
//   - AICLI_IMAGE_MAX_DIMENSION：长边上限像素；0 表示关闭压缩，负数表示不缩放。
//   - AICLI_IMAGE_MAX_MB：单张体积上限（MB）；非正数或非法值忽略。
const (
	envChatImageMaxDimension = "AICLI_IMAGE_MAX_DIMENSION"
	envChatImageMaxMB        = "AICLI_IMAGE_MAX_MB"
)

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
		MaxDimension: chatImageMaxDimension(session),
		MaxBytes:     chatImageMaxBytes(session),
	})
	if err != nil {
		return preparedChatImage{}, err
	}
	if result.Skipped {
		return preparedChatImage{Note: result.Note}, nil
	}
	return preparedChatImage{Path: result.Path, Note: result.Note}, nil
}

// chatImageMaxDimension 解析发送前图片的长边上限，优先级：
// 环境变量 AICLI_IMAGE_MAX_DIMENSION > aicli.chat.max_image_dimension > 内置默认。
// 约定（环境变量与配置一致）：0 表示"关闭压缩"，在 imageprep 的语义里对应负值
// （不缩放）；负值同样表示不缩放。这样"关闭"不会被误读成"使用默认上限"。
func chatImageMaxDimension(session *ChatSession) int {
	if raw := strings.TrimSpace(os.Getenv(envChatImageMaxDimension)); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			return normalizeChatImageMaxDimension(value)
		}
	}
	if cfg := chatImagePreferenceConfig(session); cfg != nil && cfg.MaxImageDimension != nil {
		return normalizeChatImageMaxDimension(*cfg.MaxImageDimension)
	}
	return defaultChatImageMaxDimension
}

func normalizeChatImageMaxDimension(value int) int {
	if value == 0 {
		return -1
	}
	return value
}

// chatImageMaxBytes 解析单张图片的体积上限，优先级同上（环境变量单位为 MB）。
// 未配置、非正数或非法值一律回落内置默认——不提供"完全不限制体积"的取值，
// 避免一次误操作把超大原图发出去。
func chatImageMaxBytes(session *ChatSession) int64 {
	if raw := strings.TrimSpace(os.Getenv(envChatImageMaxMB)); raw != "" {
		if mb, err := strconv.Atoi(raw); err == nil && mb > 0 {
			return int64(mb) * chatImageMBBytes
		}
	}
	if cfg := chatImagePreferenceConfig(session); cfg != nil && cfg.MaxImageMB != nil && *cfg.MaxImageMB > 0 {
		return int64(*cfg.MaxImageMB) * chatImageMBBytes
	}
	return maxChatImageBytes
}

// chatImagePreferenceConfig 取会话配置里的 aicli.chat 段；任一环节缺失都返回 nil。
func chatImagePreferenceConfig(session *ChatSession) *config.AICLIChatConfig {
	if session == nil || session.Config == nil || session.Config.AICLI == nil {
		return nil
	}
	return session.Config.AICLI.Chat
}

func chatImageArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	return chatSessionImageArtifactDir(session)
}
