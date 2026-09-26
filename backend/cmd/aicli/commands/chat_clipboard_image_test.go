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
	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"
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
	// 读取成功但路径不可用（文件被清理 / 指向目录）时必须拒绝，
	// 而不是把坏路径塞进附件——附件校验与 /attach <path> 共用同一套规则。
	missing := filepath.Join(t.TempDir(), "missing.png")
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "文件不存在", path: missing},
		{name: "路径是目录", path: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubClipboardImageRead(t, clipboardimage.Result{Path: tc.path, Width: 1, Height: 1}, nil)
			session := &ChatSession{}
			if _, err := attachClipboardImage(session, false); err == nil {
				t.Fatalf("不可用路径必须报错: %s", tc.path)
			}
			if len(session.ImagePaths) != 0 {
				t.Fatalf("不可用路径不应进入附件: %+v", session.ImagePaths)
			}
		})
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
	actionResult := controller.onActionKey(ui.LineEditorSnapshot{}, string(keymap.ActionClipboardImage))
	if !actionResult.Claimed || actionResult.ExitEditor {
		t.Fatalf("alt+v 动作应被认领且不退出编辑器: claimed=%v exit=%v", actionResult.Claimed, actionResult.ExitEditor)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("键位路径未写入附件: %+v", session.ImagePaths)
	}
	if actionResult.Replacement == nil || !strings.Contains(actionResult.Replacement.Text, "[Image #1]") {
		t.Fatalf("alt+v 成功读取后应插入图片令牌: %+v", actionResult.Replacement)
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

// writeLargeClipboardPNG 写出一张指定尺寸的实心 PNG，用于验证发送前压缩。
func writeLargeClipboardPNG(t *testing.T, width, height int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big-shot.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建大图失败: %v", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 241), B: 180, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("编码大图失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭大图失败: %v", err)
	}
	return path
}

func decodeImageSize(t *testing.T, path string) (int, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开图片失败: %v", err)
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("读取图片尺寸失败: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestAttachClipboardImageCompressesLargeImage(t *testing.T) {
	source := writeLargeClipboardPNG(t, 2000, 1000)
	stubClipboardImageRead(t, clipboardimage.Result{Path: source, Width: 2000, Height: 1000, Source: "test"}, nil)

	session := &ChatSession{SessionDir: t.TempDir()}
	message, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("attachClipboardImage 失败: %v", err)
	}
	if len(session.ImagePaths) != 1 {
		t.Fatalf("附件数量错误: %+v", session.ImagePaths)
	}
	attached := session.ImagePaths[0]
	if attached == source {
		t.Fatalf("超过长边上限的图片应被压缩到新文件: %s", attached)
	}
	if !strings.Contains(attached, filepath.Join(session.SessionDir, "images")) {
		t.Fatalf("压缩结果应落在会话 artifact 目录: %s", attached)
	}
	width, height := decodeImageSize(t, attached)
	if width > imageprep.DefaultMaxDimension || height > imageprep.DefaultMaxDimension {
		t.Fatalf("压缩后长边应在上限内: %dx%d", width, height)
	}
	if width != imageprep.DefaultMaxDimension {
		t.Fatalf("长边应缩到上限: %dx%d", width, height)
	}
	if !strings.Contains(message, "已压缩") || !strings.Contains(message, "已从剪贴板添加图片附件") {
		t.Fatalf("提示应包含压缩说明: %q", message)
	}

	// 同一张剪贴板图（内容相同、临时文件名不同）再次粘贴：按内容哈希落到同一
	// 产物路径，因此只提示"已在附件中"，不会重复入列。
	secondSource := writeLargeClipboardPNG(t, 2000, 1000)
	stubClipboardImageRead(t, clipboardimage.Result{Path: secondSource, Width: 2000, Height: 1000, Source: "test"}, nil)
	again, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("第二次 attachClipboardImage 失败: %v", err)
	}
	if len(session.ImagePaths) != 1 || session.ImagePaths[0] != attached {
		t.Fatalf("同内容重复粘贴不应新增附件: %+v", session.ImagePaths)
	}
	if !strings.Contains(again, "已在附件中") {
		t.Fatalf("重复粘贴应给出提示: %q", again)
	}
}

func TestAttachClipboardImageSkipsOversizeFile(t *testing.T) {
	huge := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(huge, []byte("x"), 0o644); err != nil {
		t.Fatalf("创建超限文件失败: %v", err)
	}
	if err := os.Truncate(huge, (32<<20)+1); err != nil {
		t.Fatalf("扩展超限文件失败: %v", err)
	}
	stubClipboardImageRead(t, clipboardimage.Result{Path: huge, Width: 10, Height: 10}, nil)

	session := &ChatSession{}
	message, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("超限应只提示、不报错: %v", err)
	}
	if len(session.ImagePaths) != 0 {
		t.Fatalf("超限图片不应进入附件: %+v", session.ImagePaths)
	}
	if !strings.Contains(message, "已跳过") || !strings.Contains(message, "上限") {
		t.Fatalf("超限提示不符: %q", message)
	}
}

func TestAttachClipboardImageCanDisableCompression(t *testing.T) {
	t.Setenv(envChatImageMaxDimension, "0")
	source := writeLargeClipboardPNG(t, 2000, 1000)
	stubClipboardImageRead(t, clipboardimage.Result{Path: source, Width: 2000, Height: 1000}, nil)

	session := &ChatSession{SessionDir: t.TempDir()}
	message, err := attachClipboardImage(session, false)
	if err != nil {
		t.Fatalf("attachClipboardImage 失败: %v", err)
	}
	if len(session.ImagePaths) != 1 || session.ImagePaths[0] != source {
		t.Fatalf("关闭压缩后应原样入附件: %+v", session.ImagePaths)
	}
	if strings.Contains(message, "已压缩") {
		t.Fatalf("关闭压缩后不应出现压缩说明: %q", message)
	}
}

func TestStructuredAttachCompressesLargeFile(t *testing.T) {
	source := writeLargeClipboardPNG(t, 2400, 1200)
	session := &ChatSession{SessionDir: t.TempDir()}

	result := executeStructuredAttachmentCommand(session, "/attach "+source)
	if len(session.ImagePaths) != 1 {
		t.Fatalf("/attach 未写入附件: %+v", session.ImagePaths)
	}
	if session.ImagePaths[0] == source {
		t.Fatal("/attach 大图应被压缩")
	}
	width, height := decodeImageSize(t, session.ImagePaths[0])
	if width > imageprep.DefaultMaxDimension || height > imageprep.DefaultMaxDimension {
		t.Fatalf("压缩后长边应在上限内: %dx%d", width, height)
	}
	if len(result.Blocks) == 0 {
		t.Fatalf("/attach 应返回文本结果: %+v", result)
	}
}

func TestChatImageMaxDimensionMapping(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{name: "未配置", env: "", want: defaultChatImageMaxDimension},
		{name: "自定义", env: "512", want: 512},
		{name: "关闭压缩", env: "0", want: -1},
		{name: "负数表示不缩放", env: "-5", want: -5},
		{name: "非法值回退默认", env: "abc", want: defaultChatImageMaxDimension},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envChatImageMaxDimension, tc.env)
			if got := chatImageMaxDimension(); got != tc.want {
				t.Fatalf("chatImageMaxDimension() = %d, want %d", got, tc.want)
			}
		})
	}
}
