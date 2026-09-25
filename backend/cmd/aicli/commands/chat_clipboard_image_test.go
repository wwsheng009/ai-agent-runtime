package commands

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/keymap"
	"github.com/wwsheng009/ai-agent-runtime/internal/clipboardimage"
)

// writeClipboardFixturePNG 写一张 2x2 PNG，作为剪贴板读取结果的替身。
func writeClipboardFixturePNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clipboard-shot.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试 PNG 失败: %v", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(60 * x), G: uint8(60 * y), B: 200, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("编码测试 PNG 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭测试 PNG 失败: %v", err)
	}
	return path
}

// stubClipboardImageRead 替换包级剪贴板读取入口，测试结束后恢复。
func stubClipboardImageRead(t *testing.T, result clipboardimage.Result, err error) {
	t.Helper()
	original := readClipboardImage
	readClipboardImage = func(context.Context, string) (clipboardimage.Result, error) {
		return result, err
	}
	t.Cleanup(func() { readClipboardImage = original })
}

func TestAttachClipboardImageAddsAndDedupes(t *testing.T) {
	path := writeClipboardFixturePNG(t)
	stubClipboardImageRead(t, clipboardimage.Result{Path: path, Width: 5, Height: 3, Source: "test"}, nil)

	session := &ChatSession{}
	message, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("attachClipboardImage 失败: %v", err)
	}
	if len(session.ImagePaths) != 1 || session.ImagePaths[0] != path {
		t.Fatalf("附件未写入会话: %+v", session.ImagePaths)
	}
	if !strings.Contains(message, "5x3") || !strings.Contains(message, "已从剪贴板添加图片附件") {
		t.Fatalf("确认信息不完整: %q", message)
	}

	// 同一路径再次粘贴：去重提示，不重复追加。
	again, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("第二次 attachClipboardImage 失败: %v", err)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("重复粘贴不应追加附件: %+v", session.ImagePaths)
	}
	if !strings.Contains(again, "已在附件中") {
		t.Fatalf("重复粘贴应给出提示: %q", again)
	}
}

func TestAttachClipboardImageRejectsInvalidFile(t *testing.T) {
	// 剪贴板读取成功但落盘内容不是合法图片：必须拒绝，而不是把坏文件塞进附件。
	broken := filepath.Join(t.TempDir(), "broken.png")
	if err := os.WriteFile(broken, []byte("not a png"), 0o644); err != nil {
		t.Fatalf("写入坏文件失败: %v", err)
	}
	stubClipboardImageRead(t, clipboardimage.Result{Path: broken, Width: 1, Height: 1}, nil)

	session := &ChatSession{}
	if _, err := attachClipboardImage(session, false); err == nil {
		t.Fatal("非法图片必须报错")
	}
	if len(session.ImagePaths) != 0 {
		t.Fatalf("非法图片不应进入附件: %+v", session.ImagePaths)
	}
}

func TestChatClipboardImageErrorMessageMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "无图片", err: clipboardimage.ErrNoImage, want: "没有图片"},
		{name: "平台不支持", err: clipboardimage.ErrUnsupported, want: "不支持"},
		{name: "超时", err: context.DeadlineExceeded, want: "超时"},
		{name: "其它错误", err: errors.New("boom"), want: "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := chatClipboardImageErrorMessage(tc.err)
			if !strings.Contains(message, tc.want) {
				t.Fatalf("错误文案 = %q, want 包含 %q", message, tc.want)
			}
			if !strings.Contains(message, "/attach") && tc.name != "其它错误" {
				t.Fatalf("错误文案应给出替代路径: %q", message)
			}
		})
	}
	if got := chatClipboardImageErrorMessage(nil); got != "" {
		t.Fatalf("nil 错误应为空文案，得到 %q", got)
	}
}

func TestStructuredAttachPasteCommand(t *testing.T) {
	path := writeClipboardFixturePNG(t)
	stubClipboardImageRead(t, clipboardimage.Result{Path: path, Width: 2, Height: 2, Source: "test"}, nil)

	session := &ChatSession{}
	result := executeStructuredAttachmentCommand(session, "/attach paste")
	if len(session.ImagePaths) != 1 {
		t.Fatalf("结构化命令未写入附件: %+v", session.ImagePaths)
	}
	if len(result.Blocks) != 1 || result.Action != CommandContinue {
		t.Fatalf("/attach paste 应返回单块文本结果: %+v", result)
	}
}

func TestStructuredAttachPasteReportsNoImage(t *testing.T) {
	stubClipboardImageRead(t, clipboardimage.Result{}, clipboardimage.ErrNoImage)
	session := &ChatSession{}
	result := executeStructuredAttachmentCommand(session, "/attach paste")
	if len(session.ImagePaths) != 0 {
		t.Fatalf("无图片时不应产生附件: %+v", session.ImagePaths)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("无图片时应返回文本结果: %+v", result)
	}
}

func TestComposerActionKeyClipboardImage(t *testing.T) {
	path := writeClipboardFixturePNG(t)
	stubClipboardImageRead(t, clipboardimage.Result{Path: path, Width: 2, Height: 2}, nil)

	session := &ChatSession{}
	controller := &chatComposerController{session: session}
	claimed, exitEditor := controller.onActionKey(ui.LineEditorSnapshot{}, string(keymap.ActionClipboardImage))
	if !claimed || exitEditor {
		t.Fatalf("alt+v 动作应被认领且不退出编辑器: claimed=%v exit=%v", claimed, exitEditor)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("键位路径未写入附件: %+v", session.ImagePaths)
	}
}

func TestKeymapCatalogDeclaresClipboardImageAction(t *testing.T) {
	for _, spec := range keymap.Catalog() {
		if spec.Action != keymap.ActionClipboardImage {
			continue
		}
		if !spec.Remappable {
			t.Fatal("剪贴板图片动作应可重映射")
		}
		if len(spec.Defaults) != 1 || spec.Defaults[0] != "alt+v" {
			t.Fatalf("默认绑定应只有 alt+v: %+v", spec.Defaults)
		}
		if !strings.Contains(spec.Description, "/attach paste") {
			t.Fatalf("动作说明应给出等价命令: %q", spec.Description)
		}
		return
	}
	t.Fatal("keymap catalog 缺少 app.attach.clipboard_image")
}
