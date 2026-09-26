package imageattach

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, dir, name string, width, height int, alpha uint8) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 20), G: uint8(y * 20), B: 128, A: alpha})
		}
	}
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试图片失败: %v", err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("编码测试图片失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("写入测试图片失败: %v", err)
	}
	return path
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestLimitsFallBackAndDefaults(t *testing.T) {
	limits := DefaultLimits()
	if limits.MaxDimension != 1568 || limits.MaxBytes != 32<<20 {
		t.Fatalf("内置上限应保持 1568px / 32MB: %+v", limits)
	}

	dir := t.TempDir()
	source := writePNG(t, dir, "small.png", 8, 8, 255)

	// 零值 Limits = 默认上限：小图不应被改动。
	zero, err := PrepareLocal(source, filepath.Join(dir, "a"), Limits{})
	if err != nil {
		t.Fatalf("零值上限处理失败: %v", err)
	}
	if zero.Path != source || zero.Skipped {
		t.Fatalf("零值上限应按默认策略原样返回: %+v", zero)
	}

	// 负数 MaxDimension = 关闭压缩：即使超过默认上限也原样返回。
	off, err := PrepareLocal(source, filepath.Join(dir, "b"), Limits{MaxDimension: -1})
	if err != nil {
		t.Fatalf("关闭压缩处理失败: %v", err)
	}
	if off.Path != source {
		t.Fatalf("关闭压缩应原样返回，得到 %s", off.Path)
	}
}

func TestPrepareLocalKeepsOriginalWithinLimits(t *testing.T) {
	dir := t.TempDir()
	source := writePNG(t, dir, "small.png", 8, 8, 255)
	prepared, err := PrepareLocal(source, filepath.Join(dir, "artifacts"), DefaultLimits())
	if err != nil {
		t.Fatalf("PrepareLocal 失败: %v", err)
	}
	if prepared.Skipped {
		t.Fatalf("不应跳过: %+v", prepared)
	}
	if prepared.Path != source {
		t.Fatalf("已在上限内应原样返回，得到 %s", prepared.Path)
	}
	if prepared.Width != 8 || prepared.Height != 8 {
		t.Fatalf("尺寸应为 8x8，得到 %dx%d", prepared.Width, prepared.Height)
	}
}

func TestPrepareLocalResizesOverDimension(t *testing.T) {
	dir := t.TempDir()
	source := writePNG(t, dir, "big.png", 8, 8, 255)
	prepared, err := PrepareLocal(source, filepath.Join(dir, "artifacts"), Limits{MaxDimension: 4})
	if err != nil {
		t.Fatalf("PrepareLocal 失败: %v", err)
	}
	if prepared.Skipped || prepared.Path == "" {
		t.Fatalf("应产出压缩后的图片: %+v", prepared)
	}
	if prepared.Path == source {
		t.Fatalf("超过长边上限应触发缩放，得到原路径")
	}
	if prepared.Width > 4 || prepared.Height > 4 {
		t.Fatalf("长边应不超过 4px，得到 %dx%d", prepared.Width, prepared.Height)
	}
}

func TestPrepareLocalAllKeepsOrderAndSkipsBadItems(t *testing.T) {
	dir := t.TempDir()
	valid := writePNG(t, dir, "ok.png", 4, 4, 255)
	missing := filepath.Join(dir, "missing.png")
	results := PrepareLocalAll([]string{valid, "  ", missing}, filepath.Join(dir, "artifacts"), DefaultLimits())
	if len(results) != 2 {
		t.Fatalf("空白项应被忽略，期望 2 项，得到 %d", len(results))
	}
	if results[0].Path != valid || results[0].Skipped {
		t.Fatalf("第一项应是可用图片: %+v", results[0])
	}
	if !results[1].Skipped || !strings.Contains(results[1].Note, "已跳过") {
		t.Fatalf("第二项应带原因跳过: %+v", results[1])
	}
}

func TestSaveUploadedIsIdempotentByContent(t *testing.T) {
	dir := t.TempDir()
	artifactDir := filepath.Join(dir, "artifacts")
	source := writePNG(t, dir, "shot.png", 8, 8, 255)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("读取测试图片失败: %v", err)
	}

	first, err := SaveUploaded(data, "shot.png", artifactDir, Limits{MaxDimension: 4})
	if err != nil {
		t.Fatalf("SaveUploaded 失败: %v", err)
	}
	second, err := SaveUploaded(data, "another-name.png", artifactDir, Limits{MaxDimension: 4})
	if err != nil {
		t.Fatalf("重复上传失败: %v", err)
	}
	if first.Path == "" || first.Skipped {
		t.Fatalf("首次上传应产出 artifact: %+v", first)
	}
	if first.Width > 4 || first.Height > 4 {
		t.Fatalf("上传应按上限缩放，得到 %dx%d", first.Width, first.Height)
	}
	if first.Path != second.Path {
		t.Fatalf("同一内容重复上传应命中同一路径（内容哈希命名）: %s vs %s", first.Path, second.Path)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("artifact 应真实存在: %v", err)
	}
	if leftovers := listDir(t, artifactDir); len(leftovers) != 1 {
		t.Fatalf("artifact 目录不应残留临时文件: %v", leftovers)
	}
}

func TestSaveUploadedRejectsOversizeBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	artifactDir := filepath.Join(dir, "artifacts")
	source := writePNG(t, dir, "shot.png", 8, 8, 255)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("读取测试图片失败: %v", err)
	}

	prepared, err := SaveUploaded(data, "shot.png", artifactDir, Limits{MaxBytes: 16})
	if err != nil {
		t.Fatalf("超限应给结果而不是错误: %v", err)
	}
	if !prepared.Skipped || prepared.Path != "" {
		t.Fatalf("超限应标记跳过且不给路径: %+v", prepared)
	}
	if !strings.Contains(prepared.Note, "超过") || !strings.Contains(prepared.Note, "shot.png") {
		t.Fatalf("提示应写明文件与上限: %q", prepared.Note)
	}
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		if entries := listDir(t, artifactDir); len(entries) != 0 {
			t.Fatalf("超限文件不应落盘: %v", entries)
		}
	}
}

func TestSaveUploadedRejectsNonImageBytes(t *testing.T) {
	dir := t.TempDir()
	_, err := SaveUploaded([]byte("这不是图片"), "note.txt", dir, DefaultLimits())
	if err == nil {
		t.Fatalf("非图片字节应报错")
	}
	if !strings.Contains(err.Error(), "不是可识别的图片") || !strings.Contains(err.Error(), "note.txt") {
		t.Fatalf("错误应说明文件与原因: %v", err)
	}
}

func TestSaveUploadedRejectsEmptyPayload(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveUploaded(nil, "empty.png", dir, DefaultLimits()); err == nil {
		t.Fatalf("空内容应报错")
	}
}

func TestSaveUploadedKeepsTransparencyAsPNG(t *testing.T) {
	dir := t.TempDir()
	artifactDir := filepath.Join(dir, "artifacts")
	source := writePNG(t, dir, "alpha.png", 8, 8, 128)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("读取测试图片失败: %v", err)
	}

	prepared, err := SaveUploaded(data, "alpha.png", artifactDir, Limits{MaxDimension: 4})
	if err != nil {
		t.Fatalf("SaveUploaded 失败: %v", err)
	}
	if prepared.Skipped {
		t.Fatalf("不应跳过: %+v", prepared)
	}
	if ext := filepath.Ext(prepared.Path); ext != ".png" {
		t.Fatalf("含透明通道的上传应保持 PNG，得到 %s", ext)
	}
}

// 回归：上传在限制内（imageprep 不重写文件）时，返回的路径必须是 artifact 目录里
// 的持久文件，而不是随后被清理的临时文件。
func TestSaveUploadedWithinLimitsKeepsDurableArtifact(t *testing.T) {
	dir := t.TempDir()
	artifactDir := filepath.Join(dir, "artifacts")
	source := writePNG(t, dir, "shot.png", 8, 8, 255)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("读取测试图片失败: %v", err)
	}

	prepared, err := SaveUploaded(data, "shot.png", artifactDir, DefaultLimits())
	if err != nil {
		t.Fatalf("SaveUploaded 失败: %v", err)
	}
	if prepared.Path == "" || prepared.Skipped {
		t.Fatalf("限制内的上传也应产出可用路径: %+v", prepared)
	}
	if filepath.Dir(prepared.Path) != artifactDir {
		t.Fatalf("落盘路径应在 artifact 目录内: %s", prepared.Path)
	}
	if _, err := os.Stat(prepared.Path); err != nil {
		t.Fatalf("返回的路径必须真实存在: %v", err)
	}

	// 同一内容重复上传（哪怕文件名不同）命中同一 artifact。
	again, err := SaveUploaded(data, "copy.png", artifactDir, DefaultLimits())
	if err != nil {
		t.Fatalf("重复上传失败: %v", err)
	}
	if again.Path != prepared.Path {
		t.Fatalf("同内容应命中同一路径: %s vs %s", prepared.Path, again.Path)
	}
	if leftovers := listDir(t, artifactDir); len(leftovers) != 1 {
		t.Fatalf("artifact 目录不应残留临时文件: %v", leftovers)
	}
}
