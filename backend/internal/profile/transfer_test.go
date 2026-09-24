package profile

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRawZip 造一个**不做任何过滤**的 zip（用于喂给 ReadBundleZip 的安全断言）。
func buildRawZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestSanitizeBundlePathAcceptsProfileShapedPaths(t *testing.T) {
	for _, p := range []string{
		"profile.yaml",
		"agents/reviewer/prompts/system.md",
		"skills/x/SKILL.md",
		"mcp.yaml",
	} {
		out, err := sanitizeBundlePath(p)
		require.NoError(t, err, p)
		assert.Equal(t, p, out)
	}
}

func TestSanitizeBundlePathRejectsTraversalAndWeirdForms(t *testing.T) {
	cases := map[string]string{
		"":                                      "empty",
		"../escape.yaml":                        "traversal",
		"a/../../b.yaml":                        "normalized",
		"a/../b.yaml":                           "normalized",
		"/abs.yaml":                             "absolute",
		"a//b.yaml":                             "normalized",
		"./a.yaml":                              "normalized",
		"a/":                                    "normalized",
		"dir\\file.yaml":                        "forbidden",
		"C:/x.yaml":                             "forbidden",
		"a:b.yaml":                              "forbidden",
		"a\x00b.yaml":                           "control",
		"a\x01b.yaml":                           "control",
		strings.Repeat("a", bundleMaxPathLen+1): "too long",
	}
	for p, want := range cases {
		_, err := sanitizeBundlePath(p)
		require.Error(t, err, "path %q 必须被拒绝", p)
		assert.Contains(t, err.Error(), want, "path %q", p)
	}
}

func TestReadBundleZipRejectsUnsafeOrNonProfileArchives(t *testing.T) {
	reader := func(raw []byte) *bytes.Reader { return bytes.NewReader(raw) }

	// 遍历路径
	raw := buildRawZip(t, map[string][]byte{
		"profile.yaml":   []byte("name: x\n"),
		"../escape.yaml": []byte("x"),
	})
	_, err := ReadBundleZip(reader(raw), int64(len(raw)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traversal")

	// 缺根 profile.yaml
	raw = buildRawZip(t, map[string][]byte{"agents/a.md": []byte("x")})
	_, err = ReadBundleZip(reader(raw), int64(len(raw)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile.yaml")

	// 大小写不敏感重名（Windows 上会互相覆盖）
	raw = buildRawZip(t, map[string][]byte{
		"profile.yaml": []byte("name: x\n"),
		"agents/a.md":  []byte("1"),
		"AGENTS/A.MD":  []byte("2"),
	})
	_, err = ReadBundleZip(reader(raw), int64(len(raw)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	// 单文件超限
	raw = buildRawZip(t, map[string][]byte{
		"profile.yaml": []byte("name: x\n"),
		"big.bin":      bytes.Repeat([]byte("a"), BundleMaxFileBytes+1),
	})
	_, err = ReadBundleZip(reader(raw), int64(len(raw)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestReadBundleZipRoundTripsCollectedFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "agents", "reviewer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml"), []byte("name: rt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents", "reviewer", "prompts.md"), []byte("hi"), 0o644))

	files, err := CollectBundleFiles(root)
	require.NoError(t, err)
	require.Len(t, files, 2)

	var buf bytes.Buffer
	require.NoError(t, WriteBundleZip(&buf, files))
	back, err := ReadBundleZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, back, 2)
	assert.Equal(t, files, back, "收集 → 打包 → 读取必须逐字节往返")
}

func TestCollectBundleFilesSkipsTempAndSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml"), []byte("name: x\n"), 0o644))
	// 原子写残留的临时文件不进包
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml.123.tmp"), []byte("junk"), 0o644))
	// 符号链接不跟随、不导出（Windows 无权限时跳过该断言）
	if err := os.Symlink(filepath.Join(root, "profile.yaml"), filepath.Join(root, "link.yaml")); err == nil {
		files, err := CollectBundleFiles(root)
		require.NoError(t, err)
		for _, f := range files {
			assert.NotEqual(t, "link.yaml", f.Path, "符号链接不该进包")
		}
	}

	files, err := CollectBundleFiles(root)
	require.NoError(t, err)
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"profile.yaml"}, paths)
}

func TestExtractBundleWritesNestedFilesAndRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	paths, err := ExtractBundle(dir, []BundleFile{
		{Path: "profile.yaml", Data: []byte("name: x\n")},
		{Path: "agents/a/prompts/system.md", Data: []byte("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agents/a/prompts/system.md", "profile.yaml"}, paths)
	raw, err := os.ReadFile(filepath.Join(dir, "agents", "a", "prompts", "system.md"))
	require.NoError(t, err)
	assert.Equal(t, "hi", string(raw))

	_, err = ExtractBundle(dir, []BundleFile{{Path: "../escape.yaml", Data: []byte("x")}})
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "escape.yaml"))
}
