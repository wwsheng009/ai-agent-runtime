package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/clipboardimage"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// readClipboardImage 是包级读取入口，测试可替换为假实现。
var readClipboardImage = clipboardimage.Read

// clipboardImageReadTimeout 是剪贴板读取的上限：避免平台命令（osascript / xclip）
// 卡住交互输入。
const clipboardImageReadTimeout = 5 * time.Second

// attachClipboardImage 读取剪贴板图片、落盘为临时 PNG 并加入待发送附件。
// refresh 为 true 时刷新 composer 上下文（命令路径），键位路径只写状态行。
func attachClipboardImage(session *ChatSession, refresh bool) (string, error) {
	if session == nil {
		return "", errors.New("当前没有活动会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardImageReadTimeout)
	defer cancel()
	result, err := readClipboardImage(ctx, "")
	if err != nil {
		return "", err
	}
	if warnings := llm.ValidateLocalInputImagePaths([]string{result.Path}); len(warnings) > 0 {
		return "", fmt.Errorf("剪贴板图片无法作为附件（%s）: %s", result.Path, warnings[0])
	}
	prepared, err := prepareChatImageAttachment(session, result.Path)
	if err != nil {
		return "", fmt.Errorf("剪贴板图片无法作为附件（%s）: %w", result.Path, err)
	}
	if prepared.Path == "" {
		// 超过体积上限：只提示，不加附件（绝不静默发送原图）。
		return prepared.Note, nil
	}
	attachPath := prepared.Path
	for _, existing := range session.ImagePaths {
		if strings.EqualFold(strings.TrimSpace(existing), attachPath) {
			return fmt.Sprintf("提示: 剪贴板图片已在附件中: %s", attachPath), nil
		}
	}
	session.ImagePaths = append(session.ImagePaths, attachPath)
	if refresh {
		refreshChatComposerContext(session)
	}
	if prepared.Note != "" {
		// 压缩说明里已含前后尺寸与体积，比重复一次原始尺寸更有用。
		return fmt.Sprintf("已从剪贴板添加图片附件: %s（%s；当前共 %d 个）", attachPath, prepared.Note, len(session.ImagePaths)), nil
	}
	return fmt.Sprintf("已从剪贴板添加图片附件: %s (%dx%d, 当前共 %d 个)", attachPath, result.Width, result.Height, len(session.ImagePaths)), nil
}

// chatClipboardImageErrorMessage 把底层错误翻译成用户能直接行动的一句话。
func chatClipboardImageErrorMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, clipboardimage.ErrNoImage):
		return "剪贴板里没有图片：请先复制截图，或改用 /attach <path>"
	case errors.Is(err, clipboardimage.ErrUnsupported):
		return "当前平台不支持读取剪贴板图片，请改用 /attach <path>"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "读取剪贴板超时（剪贴板可能被其它程序占用），请重试或改用 /attach <path>"
	default:
		return fmt.Sprintf("读取剪贴板图片失败: %v", err)
	}
}
