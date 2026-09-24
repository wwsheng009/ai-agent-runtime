package commands

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// Batch 13 slice 3：`aicli profile export/import`（§23 G5 / D28 / Q21）。
//
// 与 API 端点的共同契约（同一段执行核心 internal/profile/transfer.go）：
// 只读导出、先 validate 再原子落位、绝不自动激活、不覆盖同名、命名以包内
// profile.yaml 为权威。

func newProfileTransferCLIConfig(t *testing.T, root string) *config.Config {
	t.Helper()
	return &config.Config{
		ConfigFilePath: filepath.Join(root, "config.yaml"),
		SkillsRuntime:  &config.SkillsRuntimeConfig{},
	}
}

// seedCLIProfile 用 create 命令生成一个合法 profile，并补一个嵌套文件与原子写残留。
func seedCLIProfile(t *testing.T, cfg *config.Config, name, root string) {
	t.Helper()
	_, err := runProfileCreateCommand(cfg, profileCreateOptions{Name: name, Template: "coding", Root: root})
	require.NoError(t, err)
	extra := filepath.Join(root, "agents", "reviewer", "prompts", "system.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(extra), 0o755))
	require.NoError(t, os.WriteFile(extra, []byte("system prompt"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml.tmp"), []byte("junk"), 0o644))
}

func useTemporaryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestRunProfileExportCommandWritesZipAndSkipsJunk(t *testing.T) {
	root := t.TempDir()
	cfg := newProfileTransferCLIConfig(t, root)
	srcRoot := filepath.Join(root, "src", "cli-share")
	seedCLIProfile(t, cfg, "cli-share", srcRoot)

	zipPath := filepath.Join(root, "out", "cli-share.zip")
	result, err := runProfileExportCommand(cfg, profileExportOptions{Ref: srcRoot, Out: zipPath})
	require.NoError(t, err)
	assert.True(t, result.Written)
	assert.Equal(t, zipPath, result.Output)
	assert.Equal(t, "cli-share", result.Reference)
	assert.Equal(t, "cli-share", result.Profile, "包内声明的 name 应被回显")
	assert.Contains(t, result.Files, "profile.yaml")
	assert.Contains(t, result.Files, "agents/reviewer/prompts/system.md")
	assert.NotContains(t, result.Files, "profile.yaml.tmp", "原子写残留不该进包")

	info, err := os.Stat(zipPath)
	require.NoError(t, err)
	assert.EqualValues(t, result.Bytes, info.Size())

	file, err := os.Open(zipPath)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	files, err := profilesys.ReadBundleZip(file, info.Size())
	require.NoError(t, err, "导出的包必须能按同一实现读回")
	assert.Len(t, files, result.FileCount)

	// 名字引用（registry）与 --out 指向目录两种形态
	cfg.Profiles = &config.ProfilesConfig{Root: filepath.Dir(srcRoot)}
	byName, err := runProfileExportCommand(cfg, profileExportOptions{Ref: "cli-share", Out: root})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "cli-share.zip"), byName.Output)

	// --dry-run 不写盘
	dry, err := runProfileExportCommand(cfg, profileExportOptions{Ref: srcRoot, Out: filepath.Join(root, "dry.zip"), DryRun: true})
	require.NoError(t, err)
	assert.False(t, dry.Written)
	assert.NoFileExists(t, filepath.Join(root, "dry.zip"))
	assert.NotEmpty(t, dry.Files)
}

func TestRunProfileImportCommandRoundTripAndGuards(t *testing.T) {
	root := t.TempDir()
	cfg := newProfileTransferCLIConfig(t, root)
	srcRoot := filepath.Join(root, "src", "cli-import")
	seedCLIProfile(t, cfg, "cli-import", srcRoot)
	zipPath := filepath.Join(root, "cli-import.zip")
	_, err := runProfileExportCommand(cfg, profileExportOptions{Ref: srcRoot, Out: zipPath})
	require.NoError(t, err)

	home := useTemporaryHome(t)
	layerRoot := filepath.Join(home, ".aicli", "profiles")

	result, err := runProfileImportCommand(cfg, profileImportOptions{Path: zipPath, Layer: "user"})
	require.NoError(t, err)
	assert.True(t, result.Imported)
	assert.False(t, result.Activated, "D28：导入绝不自动激活")
	assert.Equal(t, "cli-import", result.Name)
	assert.Equal(t, filepath.Join(layerRoot, "cli-import"), result.Root)
	assert.NotEmpty(t, result.Files, "D28：必须打印将写入的路径清单")
	target := filepath.Join(layerRoot, "cli-import")
	assert.FileExists(t, filepath.Join(target, "profile.yaml"))
	imported, err := os.ReadFile(filepath.Join(target, "agents", "reviewer", "prompts", "system.md"))
	require.NoError(t, err)
	assert.Equal(t, "system prompt", string(imported))
	assert.NoFileExists(t, filepath.Join(target, "profile.yaml.tmp"))

	// 同名冲突：不覆盖
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: zipPath, Layer: "user"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "已存在")

	// 改名（与包内 name 不一致）：拒绝并指向 rename（D32）
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: zipPath, Layer: "user", Name: "cli-renamed"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename")
	assert.NoDirExists(t, filepath.Join(layerRoot, "cli-renamed"))

	// 非法层名
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: zipPath, Layer: "global"})
	require.Error(t, err)

	// --dry-run：校验并列出路径，但不落盘（project 层只是投影，不创建目录）
	dry, err := runProfileImportCommand(cfg, profileImportOptions{Path: zipPath, Layer: "project", DryRun: true})
	require.NoError(t, err)
	assert.False(t, dry.Imported)
	assert.False(t, dry.Activated)
	assert.NotEmpty(t, dry.Files)
	assert.NoDirExists(t, dry.Root, "dry-run 不得落盘")

	// 目录源（Q21：目录或 zip）
	dirImport, err := runProfileImportCommand(cfg, profileImportOptions{Path: srcRoot, Layer: "project", DryRun: true})
	require.NoError(t, err)
	assert.False(t, dirImport.Imported)
	assert.NotEmpty(t, dirImport.Files)
	assert.NoDirExists(t, dirImport.Root, "dry-run 不得落盘")
}

func TestRunProfileImportCommandRejectsUnsafeBundle(t *testing.T) {
	root := t.TempDir()
	cfg := newProfileTransferCLIConfig(t, root)
	home := useTemporaryHome(t)
	layerRoot := filepath.Join(home, ".aicli", "profiles")

	validRoot := filepath.Join(root, "src", "cli-safe")
	seedCLIProfile(t, cfg, "cli-safe", validRoot)
	validYAML, err := os.ReadFile(filepath.Join(validRoot, "profile.yaml"))
	require.NoError(t, err)

	buildZip := func(entries [][2]string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, entry := range entries {
			w, err := zw.Create(entry[0])
			require.NoError(t, err)
			_, err = w.Write([]byte(entry[1]))
			require.NoError(t, err)
		}
		require.NoError(t, zw.Close())
		path := filepath.Join(root, "unsafe-"+strings.ReplaceAll(entries[0][0], "/", "_")+".zip")
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
		return path
	}

	traversal := buildZip([][2]string{
		{"profile.yaml", string(validYAML)},
		{"../escape.yaml", "x"},
	})
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: traversal, Layer: "user"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traversal")
	assert.NoFileExists(t, filepath.Join(layerRoot, "escape.yaml"))

	missingYAML := buildZip([][2]string{{"agents/a.md", "x"}})
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: missingYAML, Layer: "user"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile.yaml")

	// 解压成功但过不了同一个 validate（D28-1）
	invalidYAML := buildZip([][2]string{
		{"profile.yaml", "name: [unclosed\n"},
		{"agents/a/prompts.md", "x"},
	})
	_, err = runProfileImportCommand(cfg, profileImportOptions{Path: invalidYAML, Layer: "user"})
	require.Error(t, err)
	entries, readErr := os.ReadDir(layerRoot)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "拒绝的导入不得留下任何目录（含临时目录）：%v", entries)
}
