package docread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// overrideMaxDocSourceBytes 把原生路径上限临时改成小值：既避免构造 64 MiB
// 文件，也能在小尺寸上精确命中 size == limit / limit+1 边界。
func overrideMaxDocSourceBytes(t *testing.T, limit int64) {
	t.Helper()
	previous := maxDocSourceBytes
	maxDocSourceBytes = limit
	t.Cleanup(func() { maxDocSourceBytes = previous })
}

// TestMaxDocSourceBytesDefaultIsSane: 生产常量本身就是上限的契约，
// 一旦被调小/删掉或调得过大，这条断言会失败。
func TestMaxDocSourceBytesDefaultIsSane(t *testing.T) {
	if maxDocSourceBytes <= 0 || maxDocSourceBytes > 256<<20 {
		t.Fatalf("maxDocSourceBytes must stay a sane positive bound, got %d", maxDocSourceBytes)
	}
}

// TestRenderNativePathsBoundary 覆盖 svg/text 两条原生渲染路径的读取上限：
// size == limit 放行，size == limit+1 拒绝，且错误里带路径、实际大小与上限。
func TestRenderNativePathsBoundary(t *testing.T) {
	const limit = 32
	overrideMaxDocSourceBytes(t, limit)

	dir := t.TempDir()
	cases := []struct {
		name string
		file string
		head string
	}{
		{"svg", "logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"text", "notes.md", "# notes\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exactBody := make([]byte, limit)
			copy(exactBody, tc.head)
			for i := len(tc.head); i < limit; i++ {
				exactBody[i] = ' '
			}
			exactPath := docreadTestWrite(t, dir, "exact-"+tc.file, exactBody)

			result, err := Render(context.Background(), exactPath)
			if err != nil {
				t.Fatalf("size == limit 应放行: %v", err)
			}
			if result.Markdown != string(exactBody) {
				t.Fatalf("Markdown = %q, want 原文", result.Markdown)
			}

			oversizedBody := append(append([]byte(nil), exactBody...), 'x')
			oversizedPath := docreadTestWrite(t, dir, "over-"+tc.file, oversizedBody)

			result, err = Render(context.Background(), oversizedPath)
			if err == nil {
				t.Fatal("size == limit+1 应拒绝")
			}
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("超限错误不应是 ErrUnsupported: %v", err)
			}
			for _, want := range []string{
				oversizedPath,
				fmt.Sprintf("%d bytes", limit+1),
				fmt.Sprintf("%d-byte limit", limit),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, 缺少 %q", err, want)
				}
			}
			if got := result.Metadata["doc_degraded"]; got != true {
				t.Fatalf("doc_degraded = %#v, want true", got)
			}
			if got := result.Metadata["doc_size"]; got != int64(limit+1) {
				t.Fatalf("doc_size = %#v, want %d", got, limit+1)
			}
		})
	}
}

// TestReadDocSourceGuardsSizeGrowthRace: stat 预检之后文件仍在增长时，
// io.LimitReader 兜底拒绝，不会把超限内容整体读进内存。
func TestReadDocSourceGuardsSizeGrowthRace(t *testing.T) {
	const limit = 8
	overrideMaxDocSourceBytes(t, limit)
	path := docreadTestWrite(t, t.TempDir(), "grown.txt", []byte("123456789")) // 9 字节 > limit

	if _, err := readDocSource(path, 0); err == nil {
		t.Fatal("实际读取超过 limit 应拒绝")
	} else {
		for _, want := range []string{path, fmt.Sprintf("%d-byte limit", limit)} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %q, 缺少 %q", err, want)
			}
		}
	}

	// size 预检分支：直接用 stat 到的大小拒绝，错误里带实际大小。
	if _, err := readDocSource(path, limit+1); err == nil {
		t.Fatal("size > limit 应拒绝")
	} else if !strings.Contains(err.Error(), fmt.Sprintf("%d bytes", limit+1)) {
		t.Fatalf("err = %q, 缺少实际大小", err)
	}
}
