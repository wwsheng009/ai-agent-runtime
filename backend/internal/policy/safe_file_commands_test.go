package policy

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssessShellSafeFileCommandAcceptsOnlySafeForms(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		command string
		allowed bool
		reason  string
	}{
		{"mkdir", "mkdir -p dist/nested", true, ""},
		{"touch", "touch notes.txt", true, ""},
		{"non-recursive rm", "rm build.log", true, ""},
		{"non-recursive rm with -f", "rm -f build.log", true, ""},
		{"cp then mv", "cp a.txt b.txt && mv b.txt c.txt", true, ""},
		{"rmdir", "rmdir empty-dir", true, ""},
		{"powershell remove-item", "Remove-Item build.log", true, ""},
		{"recursive rm", "rm -rf dist", false, ShellSafeFileReasonRecursive},
		{"combined recursive flag", "rm -fr dist", false, ShellSafeFileReasonRecursive},
		{"recursive rm of root", "rm -rf /", false, ShellSafeFileReasonRecursive},
		{"rmdir recursive", "rmdir /s dist", false, ShellSafeFileReasonRecursive},
		{"find delete", "find . -delete", false, ShellSafeFileReasonRecursive},
		{"powershell recursive", "Remove-Item -Recurse dist", false, ShellSafeFileReasonRecursive},
		{"sensitive target", "rm .env", false, ShellSafeFileReasonSensitiveTarget},
		{"outside workspace", "rm ../outside.txt", false, ShellSafeFileReasonUnsafeTarget},
		{"glob target", "rm *.log", false, ShellSafeFileReasonUnsafeTarget},
		{"unsupported command", "chmod +x run.sh", false, ShellSafeFileReasonNotSafeCommand},
		{"network command", "curl https://example.com -o out", false, ShellSafeFileReasonNotSafeCommand},
		{"output redirect", "touch a > b", false, ShellSafeFileReasonDynamicSyntax},
		{"pipeline with unsafe segment", "mkdir a | cat", false, ShellSafeFileReasonNotSafeCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assessment := AssessShellSafeFileCommand(tc.command, root)
			assert.Equal(t, tc.allowed, assessment.Allowed)
			if !tc.allowed && tc.reason != "" {
				assert.Equal(t, tc.reason, assessment.Reason)
			}
		})
	}
}

func TestAssessShellSafeFileCommandRequiresWorkspaceRoot(t *testing.T) {
	assessment := AssessShellSafeFileCommand("mkdir dist", "")
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellSafeFileReasonUnsafeTarget, assessment.Reason)
}

// TestAssessShellSafeFileCommandNullDeviceRedirection 锁定 accept-edits 快车道与
// 只读快车道一致的空设备豁免：stderr 丢弃不改变可验证的写入目标；其他平台
// 拼写与真实目标仍按动态语法拒绝。
func TestAssessShellSafeFileCommandNullDeviceRedirection(t *testing.T) {
	root := t.TempDir()
	native, foreign := " 2>/dev/null", " 2>NUL"
	if runtime.GOOS == "windows" {
		native, foreign = foreign, native
	}
	assert.True(t, AssessShellSafeFileCommand("touch notes.txt"+native, root).Allowed)

	assessment := AssessShellSafeFileCommand("touch notes.txt"+foreign, root)
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellSafeFileReasonDynamicSyntax, assessment.Reason)
}

func TestAcceptEditsSafeFileAllowedAppliesToEveryCommand(t *testing.T) {
	root := t.TempDir()
	allowed, detail := acceptEditsSafeFileAllowed([]string{"mkdir a", "touch a/file.txt"}, root)
	assert.True(t, allowed)
	assert.Contains(t, detail, "mkdir a")

	allowed, _ = acceptEditsSafeFileAllowed([]string{"mkdir a", "rm -rf a"}, root)
	assert.False(t, allowed, "one recursive delete keeps the whole batch out of the fast lane")
}
