package commands

import (
	"image"
	_ "image/jpeg" // 不透明图片会被 imageprep 转成 JPEG，这里注册解码器
	_ "image/png"
	"os"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

func intPtr(value int) *int { return &value }

// chatSessionWithImageLimits 构造只带 aicli.chat 图片上限的会话，便于测优先级。
func chatSessionWithImageLimits(dimension, megabytes *int) *ChatSession {
	return &ChatSession{Config: &config.Config{AICLI: &config.AICLIConfig{Chat: &config.AICLIChatConfig{
		MaxImageDimension: dimension,
		MaxImageMB:        megabytes,
	}}}}
}

// 长边上限优先级：环境变量 > 配置文件 > 内置默认；0 = 关闭压缩，负数 = 不缩放。
func TestChatImageMaxDimensionPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		configVal *int
		want      int
	}{
		{name: "都未配置用默认", env: "", configVal: nil, want: defaultChatImageMaxDimension},
		{name: "配置文件生效", env: "", configVal: intPtr(512), want: 512},
		{name: "配置文件 0 关闭压缩", env: "", configVal: intPtr(0), want: -1},
		{name: "配置文件负数不缩放", env: "", configVal: intPtr(-3), want: -3},
		{name: "环境变量优先于配置", env: "256", configVal: intPtr(512), want: 256},
		{name: "环境变量 0 关闭压缩", env: "0", configVal: intPtr(512), want: -1},
		{name: "非法环境变量回落配置", env: "abc", configVal: intPtr(512), want: 512},
		{name: "非法环境变量且无配置用默认", env: "abc", configVal: nil, want: defaultChatImageMaxDimension},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envChatImageMaxDimension, tc.env)
			if got := chatImageMaxDimension(chatSessionWithImageLimits(tc.configVal, nil)); got != tc.want {
				t.Fatalf("chatImageMaxDimension() = %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("无会话回落默认", func(t *testing.T) {
		t.Setenv(envChatImageMaxDimension, "")
		if got := chatImageMaxDimension(nil); got != defaultChatImageMaxDimension {
			t.Fatalf("chatImageMaxDimension(nil) = %d, want %d", got, defaultChatImageMaxDimension)
		}
	})
}

// 体积上限优先级同上（环境变量单位 MB）；非正数/非法值一律回落默认。
func TestChatImageMaxBytesPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		configVal *int
		want      int64
	}{
		{name: "都未配置用默认", env: "", configVal: nil, want: int64(maxChatImageBytes)},
		{name: "配置文件生效", env: "", configVal: intPtr(8), want: 8 << 20},
		{name: "配置文件 0 回落默认", env: "", configVal: intPtr(0), want: int64(maxChatImageBytes)},
		{name: "配置文件负数回落默认", env: "", configVal: intPtr(-8), want: int64(maxChatImageBytes)},
		{name: "环境变量优先于配置", env: "4", configVal: intPtr(8), want: 4 << 20},
		{name: "非法环境变量回落配置", env: "x", configVal: intPtr(8), want: 8 << 20},
		{name: "非正环境变量且无配置用默认", env: "-2", configVal: nil, want: int64(maxChatImageBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envChatImageMaxMB, tc.env)
			if got := chatImageMaxBytes(chatSessionWithImageLimits(nil, tc.configVal)); got != tc.want {
				t.Fatalf("chatImageMaxBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

// 配置键名锁死：aicli.chat.max_image_dimension / max_image_mb。
func TestChatImageLimitsYAMLKeys(t *testing.T) {
	const document = "aicli:\n  chat:\n    max_image_dimension: 3\n    max_image_mb: 5\n"
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(document), &cfg); err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}
	if cfg.AICLI == nil || cfg.AICLI.Chat == nil {
		t.Fatalf("未解析出 aicli.chat: %+v", cfg.AICLI)
	}
	if got := cfg.AICLI.Chat.MaxImageDimension; got == nil || *got != 3 {
		t.Fatalf("max_image_dimension = %v, want 3", got)
	}
	if got := cfg.AICLI.Chat.MaxImageMB; got == nil || *got != 5 {
		t.Fatalf("max_image_mb = %v, want 5", got)
	}
}

// 配置的长边上限必须真正影响发送前处理（而不是只被解析出来）。
func TestPrepareChatImageAttachmentHonorsConfigDimension(t *testing.T) {
	t.Setenv(envChatImageMaxDimension, "")
	t.Setenv(envChatImageMaxMB, "")
	source := writeClipboardFixturePNG(t) // 2x2
	session := chatSessionWithImageLimits(intPtr(1), nil)
	prepared, err := prepareChatImageAttachment(session, source)
	if err != nil {
		t.Fatalf("prepareChatImageAttachment 失败: %v", err)
	}
	if prepared.Path == "" {
		t.Fatalf("应产出处理后的图片: %+v", prepared)
	}
	if prepared.Path == source {
		t.Fatalf("长边上限 1px 应触发缩放，却返回原路径: %s", source)
	}
	file, err := os.Open(prepared.Path)
	if err != nil {
		t.Fatalf("打开处理后的图片失败: %v", err)
	}
	defer file.Close()
	header, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("解码处理后的图片失败: %v", err)
	}
	if header.Width > 1 || header.Height > 1 {
		t.Fatalf("处理后的长边应不超过 1px，得到 %dx%d", header.Width, header.Height)
	}
}
