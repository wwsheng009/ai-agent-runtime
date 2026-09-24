package profile

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Profile 导出/导入的共享实现（设计 §23 G5 / D28 / Q21）。API 与 CLI 走同一段
// 逻辑，避免"两个入口各写一套包格式"。
//
// 包格式（Q21 定案：目录或 zip，**不支持单文件内联**——单文件内联会引入第二套
// profile 方言）：zip 内的路径就是 profile 根目录下的相对路径（`profile.yaml`、
// `agents/...`、`prompts/...`、`skills/...`）；导入原样物化到目标根，不重排、
// 不改写内容。
//
// 安全纪律（D28 / R23）：
//   - 传输有界：文件数 / 单文件大小 / 总大小都有上限（拒绝"zip 炸弹"与超大目录）；
//   - 路径安全：拒绝绝对路径、`..`、盘符、反斜杠、控制字符与符号链接条目（zip-slip）；
//   - 只收常规文件：目录条目跳过（仅做路径检查），symlink/设备条目拒绝；
//   - 必须含根 `profile.yaml`，否则不是 profile 包。

const (
	// BundleMaxFiles 是包内文件数上限。
	BundleMaxFiles = 256
	// BundleMaxFileBytes 是包内单文件大小上限（未压缩）。
	BundleMaxFileBytes = 2 << 20
	// BundleMaxTotalBytes 是包内总大小上限（未压缩）。
	BundleMaxTotalBytes = 8 << 20
	// bundleMaxPathLen 是包内相对路径长度上限。
	bundleMaxPathLen = 200
)

// BundleFile 是包内一个文件（Path 为 slash 分隔的 profile 根相对路径）。
type BundleFile struct {
	Path string
	Data []byte
}

// CollectBundleFiles 采集 root 下可导出的文件集合（有界；跳过符号链接与原子写
// 残留的 `*.tmp`）。root 必须是 profile 根（含 profile.yaml）。
func CollectBundleFiles(root string) ([]BundleFile, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("profile root is required")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("profile root %q is not a directory", root)
	}
	var files []BundleFile
	total := 0
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// 符号链接不跟随、不导出：包应当是自包含的常规文件集合。
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// 原子写（writeProfileYAMLAtomic / writeFileAtomic）的临时文件不该进包。
		if strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		clean, sanErr := sanitizeBundlePath(rel)
		if sanErr != nil {
			return fmt.Errorf("profile file %s: %w", rel, sanErr)
		}
		stat, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if stat.Size() > BundleMaxFileBytes {
			return fmt.Errorf("profile file %s exceeds %d bytes", clean, BundleMaxFileBytes)
		}
		if len(files) >= BundleMaxFiles {
			return fmt.Errorf("profile has more than %d files", BundleMaxFiles)
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		total += len(data)
		if total > BundleMaxTotalBytes {
			return fmt.Errorf("profile exceeds %d bytes uncompressed", BundleMaxTotalBytes)
		}
		files = append(files, BundleFile{Path: clean, Data: data})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if !bundleHasRootProfileYAML(files) {
		return nil, fmt.Errorf("profile root %q has no profile.yaml", root)
	}
	sortBundleFiles(files)
	return files, nil
}

// WriteBundleZip 把文件集合写成 zip（条目名即 BundleFile.Path）。
func WriteBundleZip(w io.Writer, files []BundleFile) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		clean, err := sanitizeBundlePath(f.Path)
		if err != nil {
			return err
		}
		entry, err := zw.Create(clean)
		if err != nil {
			return err
		}
		if _, err := entry.Write(f.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// ReadBundleZip 读取 zip 包并做安全过滤（有界；拒绝遍历路径、符号链接条目与
// 大小写不敏感的重名——后者在 Windows 上会互相覆盖）。
func ReadBundleZip(r io.ReaderAt, size int64) ([]BundleFile, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("invalid profile archive: %w", err)
	}
	var files []BundleFile
	total := 0
	seen := make(map[string]string, len(zr.File))
	for _, entry := range zr.File {
		name := entry.Name
		if strings.HasSuffix(name, "/") || entry.FileInfo().IsDir() {
			if _, err := sanitizeBundlePath(strings.TrimSuffix(name, "/")); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", name, err)
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive entry %q is a symlink (rejected)", name)
		}
		clean, err := sanitizeBundlePath(name)
		if err != nil {
			return nil, fmt.Errorf("archive entry %q: %w", name, err)
		}
		key := strings.ToLower(clean)
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("archive contains duplicate paths %q and %q", prev, clean)
		}
		seen[key] = clean
		if len(files) >= BundleMaxFiles {
			return nil, fmt.Errorf("archive has more than %d files", BundleMaxFiles)
		}
		if entry.UncompressedSize64 > BundleMaxFileBytes {
			return nil, fmt.Errorf("archive entry %s exceeds %d bytes", clean, BundleMaxFileBytes)
		}
		rc, openErr := entry.Open()
		if openErr != nil {
			return nil, openErr
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, BundleMaxFileBytes+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > BundleMaxFileBytes {
			return nil, fmt.Errorf("archive entry %s exceeds %d bytes", clean, BundleMaxFileBytes)
		}
		total += len(data)
		if total > BundleMaxTotalBytes {
			return nil, fmt.Errorf("archive exceeds %d bytes uncompressed", BundleMaxTotalBytes)
		}
		files = append(files, BundleFile{Path: clean, Data: data})
	}
	if !bundleHasRootProfileYAML(files) {
		return nil, fmt.Errorf("archive is not a profile bundle: profile.yaml is missing at the archive root")
	}
	sortBundleFiles(files)
	return files, nil
}

// ExtractBundle 把文件集合物化到 dir（dir 应为专用空目录，调用方负责最终落位）。
// 返回写入的相对路径清单（slash 形式，已排序）——与删除端点的"路径清单"同一投影。
func ExtractBundle(dir string, files []BundleFile) ([]string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("extract directory is required")
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		clean, err := sanitizeBundlePath(f.Path)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		if !bundlePathWithin(dir, target) {
			return nil, fmt.Errorf("archive entry %q escapes the profile directory", clean)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, f.Data, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, clean)
	}
	sort.Strings(paths)
	return paths, nil
}

// sanitizeBundlePath 校验并规范化包内相对路径（slash 分隔）：拒绝绝对路径、
// 盘符/反斜杠/Windows 非法字符、控制字符、`..`、非规范形式（`a//b`、`./a`、
// 尾斜杠）与超长路径。
func sanitizeBundlePath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	if len(raw) > bundleMaxPathLen {
		return "", fmt.Errorf("path too long (%d > %d)", len(raw), bundleMaxPathLen)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	if strings.ContainsAny(raw, "\\:*?\"<>|") {
		return "", fmt.Errorf("path contains forbidden characters")
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 }) {
		return "", fmt.Errorf("path contains control characters")
	}
	clean := path.Clean(raw)
	if clean != raw {
		return "", fmt.Errorf("path must be already normalized (got %q, want %q)", raw, clean)
	}
	for _, element := range strings.Split(clean, "/") {
		if element == "" || element == "." || element == ".." {
			return "", fmt.Errorf("path traversal is not allowed")
		}
	}
	return clean, nil
}

func bundleHasRootProfileYAML(files []BundleFile) bool {
	for _, f := range files {
		if f.Path == "profile.yaml" {
			return true
		}
	}
	return false
}

func sortBundleFiles(files []BundleFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

// bundlePathWithin 报告 target 是否落在 parent 之内（防 zip-slip 的兜底检查，
// 与 api/skills 的 pathWithinDir 同一语义）。
func bundlePathWithin(parent, target string) bool {
	parent = filepath.Clean(parent)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(parent, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
