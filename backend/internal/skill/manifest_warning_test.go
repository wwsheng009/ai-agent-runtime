package skill

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归：非标准 skill 文件只走告警出口，不产生裸 stdout 字节——TUI 活动时
// 裸写终端会覆盖底部保留区（2026-10-18 deskctl 实例：fmt.Printf 直接上屏）。
// 同时锁定合法邻居仍在同一次扫描中被解析。
func TestParseSummaryDir_NonStandardFileWarnsWithoutStdoutWrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# desktop-basic\n\nno frontmatter here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(`name: manifest-warning-neighbor
description: valid neighbor
triggers:
  - type: keyword
    values: ["manifest"]
    weight: 1
`), 0o644))

	origStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writeEnd
	defer func() { os.Stdout = origStdout }()

	var warnings []string
	parser := NewManifestParser()
	parser.SetWarningHandler(func(line string) { warnings = append(warnings, line) })
	summaries, parseErr := parser.ParseSummaryDir(dir)

	os.Stdout = origStdout
	require.NoError(t, writeEnd.Close())
	captured, readErr := io.ReadAll(readEnd)
	require.NoError(t, readErr)
	_ = readEnd.Close()

	require.NoError(t, parseErr)
	require.Len(t, summaries, 1)
	require.Equal(t, "manifest-warning-neighbor", summaries[0].Name)
	require.Empty(t, strings.TrimSpace(string(captured)), "non-standard skill files must not write raw stdout")
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "SKILL.md")
}
