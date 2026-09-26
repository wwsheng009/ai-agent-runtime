package imageprep

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeImage 写出一张测试图片；encode 决定格式（png/jpeg）。
func writeImage(t *testing.T, dir, name string, img image.Image, encode func(*os.File, image.Image) error) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试图片失败: %v", err)
	}
	if err := encode(file, img); err != nil {
		t.Fatalf("编码测试图片失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭测试图片失败: %v", err)
	}
	return path
}

func solidImage(width, height int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestFitWithin(t *testing.T) {
	cases := []struct {
		width, height, max int
		wantW, wantH       int
	}{
		{width: 100, height: 50, max: 1568, wantW: 100, wantH: 50},
		{width: 3000, height: 2000, max: 1568, wantW: 1568, wantH: 1045},
		{width: 2000, height: 3000, max: 1568, wantW: 1045, wantH: 1568},
		{width: 4000, height: 4000, max: 1568, wantW: 1568, wantH: 1568},
		{width: 10000, height: 1, max: 1568, wantW: 1568, wantH: 1},
	}
	for _, tc := range cases {
		gotW, gotH := fitWithin(tc.width, tc.height, tc.max)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Fatalf("fitWithin(%d,%d,%d) = %dx%d, want %dx%d", tc.width, tc.height, tc.max, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

func TestPrepareKeepsSmallImageUntouched(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := writeImage(t, srcDir, "small.png", solidImage(100, 50, color.NRGBA{R: 10, G: 20, B: 30, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})

	result, err := Prepare(src, outDir, Options{})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if result.Rewritten {
		t.Fatalf("小图不应重写: %+v", result)
	}
	if result.Path != src || result.Width != 100 || result.Height != 50 || result.Format != "png" {
		t.Fatalf("小图结果不符: %+v", result)
	}
	if result.Note != "" {
		t.Fatalf("无需处理时不应有提示: %q", result.Note)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("读取输出目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("无需处理时不应写入文件: %d 个", len(entries))
	}
}

func TestPrepareNeverUpscales(t *testing.T) {
	src := writeImage(t, t.TempDir(), "tiny.png", solidImage(20, 10, color.NRGBA{R: 200, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	result, err := Prepare(src, t.TempDir(), Options{MaxDimension: 1568})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if result.Rewritten || result.Width != 20 || result.Height != 10 {
		t.Fatalf("不允许放大: %+v", result)
	}
}

func TestPrepareDownscalesToJPEGWithoutAlpha(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	src := writeImage(t, srcDir, "big.png", solidImage(3000, 2000, color.NRGBA{R: 200, G: 40, B: 40, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})

	result, err := Prepare(src, outDir, Options{MaxDimension: 200})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if !result.Rewritten || result.Format != "jpeg" {
		t.Fatalf("应转码为 JPEG: %+v", result)
	}
	if result.Width != 200 || result.Height != 133 {
		t.Fatalf("缩放尺寸错误: %dx%d", result.Width, result.Height)
	}
	if filepath.Dir(result.Path) != outDir {
		t.Fatalf("输出应落在指定目录: %s", result.Path)
	}
	info, err := os.Stat(result.Path)
	if err != nil || info.Size() != result.Bytes {
		t.Fatalf("输出文件与结果不一致: %v size=%d bytes=%d", err, info.Size(), result.Bytes)
	}
	if !strings.Contains(result.Note, "已压缩 3000x2000 → 200x133") || !strings.Contains(result.Note, "JPEG") {
		t.Fatalf("压缩说明不符: %q", result.Note)
	}
	// 输出必须是可解码的 JPEG，且尺寸与结果一致。
	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatalf("打开输出失败: %v", err)
	}
	defer file.Close()
	decoded, format, err := image.Decode(file)
	if err != nil {
		t.Fatalf("解码输出失败: %v", err)
	}
	if format != "jpeg" || decoded.Bounds().Dx() != 200 || decoded.Bounds().Dy() != 133 {
		t.Fatalf("输出格式/尺寸错误: %s %v", format, decoded.Bounds())
	}
}

func TestPrepareKeepsAlphaAsPNG(t *testing.T) {
	src := writeImage(t, t.TempDir(), "alpha.png", solidImage(2000, 1000, color.NRGBA{R: 10, G: 200, B: 10, A: 0}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	result, err := Prepare(src, t.TempDir(), Options{MaxDimension: 100})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if !result.Rewritten || result.Format != "png" {
		t.Fatalf("含透明通道应保持 PNG: %+v", result)
	}
	if !strings.Contains(result.Note, "PNG") {
		t.Fatalf("说明应写明 PNG: %q", result.Note)
	}
	file, err := os.Open(result.Path)
	if err != nil {
		t.Fatalf("打开输出失败: %v", err)
	}
	defer file.Close()
	decoded, format, err := image.Decode(file)
	if err != nil || format != "png" {
		t.Fatalf("输出应为 PNG: %v %s", err, format)
	}
	if _, _, _, alpha := decoded.At(0, 0).RGBA(); alpha != 0 {
		t.Fatalf("透明通道应保留，得到 alpha=%d", alpha)
	}
}

func TestPrepareSkipsOversizeFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if err := os.Truncate(src, (32<<20)+1); err != nil {
		t.Fatalf("扩展文件失败: %v", err)
	}
	result, err := Prepare(src, t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("超限不应返回错误（由调用方提示）: %v", err)
	}
	if !result.Skipped || result.Path != "" {
		t.Fatalf("超限文件应被跳过: %+v", result)
	}
	if !strings.Contains(result.Note, "上限") || !strings.Contains(result.Note, "已跳过") {
		t.Fatalf("跳过说明不符: %q", result.Note)
	}

	// 自定义上限同样生效。
	small := writeImage(t, t.TempDir(), "shot.png", solidImage(50, 50, color.NRGBA{A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	result, err = Prepare(small, t.TempDir(), Options{MaxBytes: 10})
	if err != nil {
		t.Fatalf("自定义上限失败: %v", err)
	}
	if !result.Skipped {
		t.Fatalf("自定义上限应生效: %+v", result)
	}
}

func TestPrepareRejectsCorruptFile(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "broken.png")
	if err := os.WriteFile(broken, []byte("not an image at all"), 0o644); err != nil {
		t.Fatalf("写入坏文件失败: %v", err)
	}
	if _, err := Prepare(broken, t.TempDir(), Options{}); err == nil {
		t.Fatal("损坏的图片必须报错")
	}
	if _, err := Prepare(filepath.Join(t.TempDir(), "missing.png"), t.TempDir(), Options{}); err == nil {
		t.Fatal("不存在的图片必须报错")
	}
	if _, err := Prepare(t.TempDir(), t.TempDir(), Options{}); err == nil {
		t.Fatal("目录必须报错")
	}
}

func TestPrepareNoResizeOptionKeepsOriginalSize(t *testing.T) {
	src := writeImage(t, t.TempDir(), "big.jpg", solidImage(3000, 2000, color.NRGBA{R: 1, G: 2, B: 3, A: 255}), func(f *os.File, img image.Image) error {
		return jpeg.Encode(f, img, &jpeg.Options{Quality: 80})
	})
	result, err := Prepare(src, t.TempDir(), Options{MaxDimension: -1})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if result.Rewritten || result.Path != src || result.Format != "jpeg" {
		t.Fatalf("负数上限表示不缩放: %+v", result)
	}
}

func TestPrepareDistinguishesSameNamedSources(t *testing.T) {
	outDir := t.TempDir()
	first := writeImage(t, t.TempDir(), "shot.png", solidImage(2000, 1000, color.NRGBA{R: 255, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	second := writeImage(t, t.TempDir(), "shot.png", solidImage(2000, 1000, color.NRGBA{B: 255, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	firstResult, err := Prepare(first, outDir, Options{MaxDimension: 100})
	if err != nil {
		t.Fatalf("Prepare 1 失败: %v", err)
	}
	secondResult, err := Prepare(second, outDir, Options{MaxDimension: 100})
	if err != nil {
		t.Fatalf("Prepare 2 失败: %v", err)
	}
	if firstResult.Path == secondResult.Path {
		t.Fatalf("同名不同来源的图片不应互相覆盖: %s", firstResult.Path)
	}

	// 同一内容来自不同临时文件时，应落到同一路径（上层按路径去重即幂等）。
	clone := writeImage(t, t.TempDir(), "shot.png", solidImage(2000, 1000, color.NRGBA{R: 255, A: 255}), func(f *os.File, img image.Image) error {
		return png.Encode(f, img)
	})
	cloneResult, err := Prepare(clone, outDir, Options{MaxDimension: 100})
	if err != nil {
		t.Fatalf("Prepare clone 失败: %v", err)
	}
	if cloneResult.Path != firstResult.Path {
		t.Fatalf("同内容应复用同一产物路径: %s vs %s", cloneResult.Path, firstResult.Path)
	}
}

// TestDownscaleAveragesBlocks 用 4x4 的四色块缩到 2x2，验证面积平均、通道顺序与
// 方向都没有搞反（每块内颜色一致，缩放结果必须精确等于块颜色）。
func TestDownscaleAveragesBlocks(t *testing.T) {
	colors := [4]color.NRGBA{
		{R: 255, G: 0, B: 0, A: 255},     // 左上
		{R: 0, G: 255, B: 0, A: 255},     // 右上
		{R: 0, G: 0, B: 255, A: 255},     // 左下
		{R: 255, G: 255, B: 255, A: 255}, // 右下
	}
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			index := 0
			if x >= 2 {
				index++
			}
			if y >= 2 {
				index += 2
			}
			src.SetNRGBA(x, y, colors[index])
		}
	}
	dst := downscale(src, 2, 2)
	for dy := 0; dy < 2; dy++ {
		for dx := 0; dx < 2; dx++ {
			index := 0
			if dx >= 1 {
				index++
			}
			if dy >= 1 {
				index += 2
			}
			want := colors[index]
			got := color.NRGBAModel.Convert(dst.At(dx, dy)).(color.NRGBA)
			if got != want {
				t.Fatalf("像素(%d,%d) = %+v, want %+v", dx, dy, got, want)
			}
		}
	}
}

// TestPrepareResizesEvenWhenReencodeGrows 锁定一条容易被"省字节"直觉破坏的语义：
// 扁平/合成类大图（PNG 压得极小）重编码成 JPEG 会变大，但长边上限是硬规则，
// 必须照样缩——否则这类图片会永远绕过尺寸上限。
func TestPrepareResizesEvenWhenReencodeGrows(t *testing.T) {
	// 生成一张体积很小但像素很大的渐进色 PNG。
	srcPath := filepath.Join(t.TempDir(), "flat.png")
	file, err := os.Create(srcPath)
	if err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 4000, 3000))
	for y := 0; y < 3000; y++ {
		for x := 0; x < 4000; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 241), B: 180, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("编码图片失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭图片失败: %v", err)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		t.Fatalf("读取源文件信息失败: %v", err)
	}

	result, err := Prepare(srcPath, t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if !result.Rewritten {
		t.Fatalf("超过长边上限必须缩放，即便重编码后体积更大: %+v", result)
	}
	if result.Width != DefaultMaxDimension {
		t.Fatalf("长边应缩到 %d，得到 %d", DefaultMaxDimension, result.Width)
	}
	if !strings.Contains(result.Note, "已压缩") {
		t.Fatalf("说明应写明压缩与体积变化: %q", result.Note)
	}
	if result.Bytes > info.Size() {
		t.Logf("该合成图重编码后确实变大（%s → %s），说明已如实写出", humanBytes(info.Size()), humanBytes(result.Bytes))
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:                     "512B",
		2 << 10:                 "2.0KB",
		3 << 20:                 "3.0MB",
		(3 << 20) + (512 << 10): "3.5MB",
	}
	for size, want := range cases {
		if got := humanBytes(size); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", size, got, want)
		}
	}
}
