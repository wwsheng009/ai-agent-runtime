package shellrisk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssessRiskyCommands(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name    string
		command string
	}{
		{"root", "rm -rf /"},
		{"root wildcard", "rm -rf /*"},
		{"root no preserve", "rm -rf / --no-preserve-root"},
		{"tilde", "rm -rf ~"},
		{"tilde slash", "rm -rf ~/"},
		{"tilde wildcard", "rm -rf ~/*"},
		{"home var", "rm -rf $HOME"},
		{"home var quoted", `rm -rf "$HOME"`},
		{"home var braces", "rm -rf ${HOME}"},
		{"home var child", "rm -rf $HOME/work"},
		{"flags separated", "rm -r -f /"},
		{"flags reversed", "rm -fr /"},
		{"long flags", "rm --recursive --force /"},
		{"env prefix", "FOO=bar rm -rf /"},
		{"timeout wrapper", "timeout 5 rm -rf /"},
		{"timeout signal", "timeout -s KILL 5 rm -rf /"},
		{"nice wrapper", "nice -n 5 rm -rf /"},
		{"env wrapper", "env FOO=1 rm -rf /"},
		{"nohup wrapper", "nohup rm -rf ~"},
		{"shell -c single", "bash -c 'rm -rf /'"},
		{"shell -c double", `sh -c "rm -rf /"`},
		{"cmd /c", `cmd /c "rm -rf ~"`},
		{"command substitution", "echo $(rm -rf /)"},
		{"backtick substitution", "echo `rm -rf /`"},
		{"segmented", "git status && rm -rf /"},
		{"pipelines", "true | rm -rf $HOME"},
		{"find root", "find / -delete"},
		{"find home", "find ~ -name x -delete"},
		{"userprofile variable", "rm -rf %USERPROFILE%"},
		{"powershell env variable", `rm -rf "$env:USERPROFILE"`},
		{"quote smuggled", `bash -c 'echo ok ; rm -rf /'`},
	}
	if home != "" {
		cases = append(cases,
			struct {
				name    string
				command string
			}{"literal home", "rm -rf " + home},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assessment := Assess(tc.command)
			if !assessment.Risky() {
				t.Fatalf("Assess(%q) = clean, want root/home removal finding", tc.command)
			}
			if got := assessment.PrimaryRisk(); got != RiskRootHomeRemoval {
				t.Fatalf("Assess(%q) risk = %q, want %q", tc.command, got, RiskRootHomeRemoval)
			}
		})
	}
}

func TestAssessCleanCommands(t *testing.T) {
	cases := []string{
		"rm -rf ./node_modules",
		"rm -rf build",
		"rm -rf /tmp/build",
		"rm -rf C:/tmp/output",
		"rm -f notes.txt",
		"rm notes.txt",
		"git status",
		"cat README.md",
		`echo "rm -rf /"`,
		"find ./build -name '*.tmp' -delete",
		"ls -la",
		"rm -rf node_modules dist",
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			assessment := Assess(command)
			if assessment.Risky() {
				finding, _ := assessment.First()
				t.Fatalf("Assess(%q) = risky (%+v), want clean", command, finding)
			}
		})
	}
}

func TestAssessWindowsForms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific command forms")
	}
	home, _ := os.UserHomeDir()
	cases := []string{
		`Remove-Item -Recurse -Force C:\`,
		`del /s /q C:\`,
		`rm -rf C:\`,
	}
	if home != "" {
		cases = append(cases, `Remove-Item -Recurse -Force `+home)
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			if assessment := Assess(command); !assessment.Risky() {
				t.Fatalf("Assess(%q) = clean, want risky", command)
			}
		})
	}
}

func TestAssessBatch(t *testing.T) {
	assessment := AssessCommands([]string{"git status", "rm -rf /"})
	if !assessment.Risky() {
		t.Fatal("batch with one destructive entry must be risky")
	}
	if clean := AssessCommands([]string{"git status", "ls"}); clean.Risky() {
		t.Fatal("read-only batch must be clean")
	}
}

// TestIsRecursiveRemoveFlagForms pins the shell-shape classifier that replaced
// the old "any single-dash token containing r" scan. It deliberately runs on
// every platform: PowerShell runs on Linux too and aliases rm/rd/rmdir/del to
// Remove-Item, so the decision must not be gated on runtime.GOOS.
func TestIsRecursiveRemoveFlagForms(t *testing.T) {
	recursion := []string{
		// POSIX short clusters.
		"rm -rf x", "rm -fr x", "rm -R x", "rm -r -f x", "rm -rv x", "rm -fR x",
		// GNU long form.
		"rm --recursive x",
		// PowerShell long form and its unambiguous prefixes.
		"Remove-Item -Recurse x", "Remove-Item -recurse x",
		"Remove-Item -Rec x", "Remove-Item -r x",
		// PowerShell switch carrying an explicit value.
		"Remove-Item -Recurse:$true x",
		// cmd.exe recursive switches.
		"del /s x", "rd /s x", "rmdir /S x",
	}
	for _, command := range recursion {
		if !recursionFlag(t, command) {
			t.Errorf("recursion not detected for %q", command)
		}
	}
	nonRecursion := []string{
		// PowerShell long parameters that merely contain 'r'.
		"Remove-Item -Force x", "Remove-Item -ErrorAction SilentlyContinue x",
		"Remove-Item -LiteralPath x", "Remove-Item -Verbose x",
		"Remove-Item -Confirm x", "Remove-Item -Stream x",
		// POSIX non-recursive flags.
		"rm -f x", "rm -v x", "rm -i x", "rm -d x", "rm -p x",
		// Plain forms.
		"rm x", "Remove-Item x", "del x", "rd x", "rmdir x",
	}
	for _, command := range nonRecursion {
		if recursionFlag(t, command) {
			t.Errorf("false recursion for %q", command)
		}
	}
}

// recursionFlag tokenizes the first segment of command and asks the classifier.
func recursionFlag(t *testing.T, command string) bool {
	t.Helper()
	segments, _ := Segments(command)
	if len(segments) == 0 {
		return false
	}
	fields := Fields(segments[0])
	if len(fields) < 2 {
		return false
	}
	return IsRecursiveRemove(fields[1:])
}

// TestAssessNonRecursiveDeleteUnderHomeIsClean pins the 2026-10-13 false
// positive end-to-end (session_20261013175416_O9Ughlac): a non-recursive
// removal under home is not a home removal, even though -Force/-ErrorAction/
// -LiteralPath contain an 'r'. The recursive forms must still trip.
func TestAssessNonRecursiveDeleteUnderHomeIsClean(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory")
	}
	target := filepath.Join(home, "sched-commit-msg.txt")
	for _, command := range []string{
		`Remove-Item -Force "` + target + `"`,
		`Remove-Item -ErrorAction SilentlyContinue "` + target + `"`,
		`Remove-Item -LiteralPath "` + target + `"`,
		`rm -f "` + target + `"`,
	} {
		if assessment := Assess(command); assessment.Risky() {
			finding, _ := assessment.First()
			t.Fatalf("Assess(%q) = risky (%+v), want clean: a non-recursive delete is not home removal", command, finding)
		}
	}
	for _, command := range []string{
		`Remove-Item -Recurse -Force "` + target + `"`,
		`rd /s "` + target + `"`,
		`rm -rf "` + target + `"`,
	} {
		if assessment := Assess(command); !assessment.Risky() {
			t.Fatalf("Assess(%q) = clean, want root/home removal", command)
		}
	}
}

func TestAssessUnparsed(t *testing.T) {
	assessment := Assess(`rm -rf "unbalanced`)
	if !assessment.Unparsed {
		t.Fatalf("unbalanced quote must set Unparsed, got %+v", assessment)
	}
}

func TestSegments(t *testing.T) {
	segments, ok := Segments("git status && cat a.txt | head -5; echo done")
	if !ok {
		t.Fatal("plain compound command must parse")
	}
	want := []string{"git status", "cat a.txt", "head -5", "echo done"}
	if len(segments) != len(want) {
		t.Fatalf("segments = %#v, want %#v", segments, want)
	}
	for i := range want {
		if segments[i] != want[i] {
			t.Fatalf("segments[%d] = %q, want %q", i, segments[i], want[i])
		}
	}
	quoted, ok := Segments(`echo "a && b" || ls`)
	if !ok || len(quoted) != 2 || quoted[0] != `echo "a && b"` || quoted[1] != "ls" {
		t.Fatalf("quoted separators parsed wrong: %#v ok=%v", quoted, ok)
	}
}

func TestFields(t *testing.T) {
	fields := Fields(`echo $(rm -rf /) "two words" plain`)
	if len(fields) != 4 {
		t.Fatalf("fields = %#v, want 4 entries", fields)
	}
	if fields[1] != "$(rm -rf /)" {
		t.Fatalf("substitution must stay one token, got %q", fields[1])
	}
	if fields[2] != "two words" {
		t.Fatalf("quoted arg = %q, want %q", fields[2], "two words")
	}
}

func TestNestedSubstitutionIsDetected(t *testing.T) {
	assessment := Assess(`echo "$(sh -c 'rm -rf /')"`)
	if !assessment.Risky() {
		t.Fatal("nested shell payload inside substitution must be detected")
	}
}

func TestReasonContainsTarget(t *testing.T) {
	assessment := Assess("rm -rf /")
	finding, ok := assessment.First()
	if !ok {
		t.Fatal("expected finding")
	}
	if strings.TrimSpace(finding.Target) == "" || strings.TrimSpace(finding.Segment) == "" {
		t.Fatalf("finding must carry segment and target: %+v", finding)
	}
}
