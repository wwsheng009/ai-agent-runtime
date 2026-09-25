package clipboardimage

import (
	"context"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestReadPlatformEndToEnd 是真实的剪贴板端到端验证（默认跳过）：
// 先用系统工具把一张已知图案放进剪贴板，再设置 AICLI_CLIPBOARD_E2E=1
// （可选 AICLI_CLIPBOARD_E2E_WIDTH/HEIGHT 断言尺寸）运行本测试。
//
// 例（Windows）：powershell -STA -File clip_set_image.ps1 后
//
//	AICLI_CLIPBOARD_E2E=1 go test ./internal/clipboardimage/ -run EndToEnd -v
func TestReadPlatformEndToEnd(t *testing.T) {
	if os.Getenv("AICLI_CLIPBOARD_E2E") == "" {
		t.Skip("设置 AICLI_CLIPBOARD_E2E=1 后运行（需要剪贴板里已有图片）")
	}
	available, reason := Availability()
	if !available {
		t.Skipf("当前平台不支持读取剪贴板图片: %s", reason)
	}
	result, err := Read(context.Background(), "")
	if err != nil {
		if IsNoImage(err) {
			t.Skipf("剪贴板里没有图片: %v", err)
		}
		t.Fatalf("Read 失败: %v", err)
	}
	defer func() { _ = os.Remove(result.Path) }()

	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatalf("临时 PNG 不可读: %v", err)
	}
	defer func() { _ = file.Close() }()
	decoded, err := png.Decode(file)
	if err != nil {
		t.Fatalf("临时文件不是合法 PNG: %v", err)
	}
	if decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
		t.Fatalf("尺寸与结果不一致: %v vs %dx%d", decoded.Bounds(), result.Width, result.Height)
	}
	if want := os.Getenv("AICLI_CLIPBOARD_E2E_WIDTH"); want != "" {
		value, convErr := strconv.Atoi(want)
		if convErr != nil {
			t.Fatalf("AICLI_CLIPBOARD_E2E_WIDTH 非法: %v", convErr)
		}
		if result.Width != value {
			t.Fatalf("宽度不符: got %d want %d", result.Width, value)
		}
	}
	if want := os.Getenv("AICLI_CLIPBOARD_E2E_HEIGHT"); want != "" {
		value, convErr := strconv.Atoi(want)
		if convErr != nil {
			t.Fatalf("AICLI_CLIPBOARD_E2E_HEIGHT 非法: %v", convErr)
		}
		if result.Height != value {
			t.Fatalf("高度不符: got %d want %d", result.Height, value)
		}
	}
	// AICLI_CLIPBOARD_E2E_EXPECT="x,y=r,g,b;x,y=r,g,b"：校验通道顺序与行方向。
	if expect := os.Getenv("AICLI_CLIPBOARD_E2E_EXPECT"); expect != "" {
		for _, entry := range strings.Split(expect, ";") {
			entry = strings.TrimSpace(entry)
			coords, rgb, found := strings.Cut(entry, "=")
			if !found {
				t.Fatalf("AICLI_CLIPBOARD_E2E_EXPECT 片段非法: %q", entry)
			}
			xText, yText, found := strings.Cut(coords, ",")
			if !found {
				t.Fatalf("AICLI_CLIPBOARD_E2E_EXPECT 坐标非法: %q", coords)
			}
			x, errX := strconv.Atoi(strings.TrimSpace(xText))
			y, errY := strconv.Atoi(strings.TrimSpace(yText))
			if errX != nil || errY != nil {
				t.Fatalf("AICLI_CLIPBOARD_E2E_EXPECT 坐标非法: %q", coords)
			}
			parts := strings.Split(rgb, ",")
			if len(parts) != 3 {
				t.Fatalf("AICLI_CLIPBOARD_E2E_EXPECT 颜色非法: %q", rgb)
			}
			want := make([]uint8, 3)
			for index, part := range parts {
				value, convErr := strconv.Atoi(strings.TrimSpace(part))
				if convErr != nil {
					t.Fatalf("AICLI_CLIPBOARD_E2E_EXPECT 颜色非法: %q", rgb)
				}
				want[index] = uint8(value)
			}
			pixel := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
			if pixel.R != want[0] || pixel.G != want[1] || pixel.B != want[2] {
				t.Fatalf("像素 (%d,%d) 不符: got %d,%d,%d want %d,%d,%d", x, y,
					pixel.R, pixel.G, pixel.B, want[0], want[1], want[2])
			}
		}
	}
	t.Logf("剪贴板图片读取成功: %dx%d source=%s path=%s", result.Width, result.Height, result.Source, result.Path)
}
