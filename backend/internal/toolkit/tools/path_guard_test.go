package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathGuardUnsupportedPathNameReason(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		reject bool
		expect string // optional substring that must appear in the reason
	}{
		// Windows reserved device names, in the spellings models emit.
		{name: "windows nul lowercase", path: "nul", reject: true, expect: "NUL"},
		{name: "windows nul uppercase", path: "NUL", reject: true, expect: "NUL"},
		{name: "windows nul with extension", path: "NUL.txt", reject: true, expect: "NUL"},
		{name: "windows nul trailing space", path: "NUL ", reject: true, expect: "NUL"},
		{name: "windows nul trailing dot", path: "NUL.", reject: true, expect: "NUL"},
		{name: "windows com1", path: "COM1", reject: true, expect: "COM1"},
		{name: "windows lpt9", path: "LPT9", reject: true, expect: "LPT9"},
		{name: "windows path segment nul", path: `C:\src\nul`, reject: true, expect: "NUL"},
		{name: "windows slash segment con", path: "src/con/readme.txt", reject: true, expect: "CON"},
		{name: "windows device namespace", path: `\\.\PhysicalDrive0`, reject: true, expect: "device namespace"},
		{name: "windows globalroot namespace", path: `\\?\GLOBALROOT\Device\HarddiskVolume1`, reject: true, expect: "GLOBALROOT"},
		{name: "windows device namespace slash spelling", path: "//./PhysicalDrive0", reject: true, expect: "device namespace"},

		// Near misses that must stay readable.
		{name: "windows com10 not reserved", path: "COM10", reject: false},
		{name: "windows lpt10 not reserved", path: "LPT10", reject: false},
		{name: "windows com10 in path", path: `C:\src\COM10.txt`, reject: false},
		{name: "windows nul-like name not reserved", path: "NULLl", reject: false},
		{name: "windows config not reserved", path: "CONFIG", reject: false},
		{name: "windows auxiliary not reserved", path: "auxiliary.txt", reject: false},
		{name: "windows extended path not device namespace", path: `\\?\C:\src\main.go`, reject: false},

		// Unix device / pseudo-file namespace.
		{name: "unix dev zero", path: "/dev/zero", reject: true, expect: "/dev/zero"},
		{name: "unix dev random", path: "/dev/random", reject: true, expect: "/dev/random"},
		{name: "unix dev urandom", path: "/dev/urandom", reject: true, expect: "/dev/urandom"},
		{name: "unix dev full", path: "/dev/full", reject: true, expect: "/dev/full"},
		{name: "unix dev stdin", path: "/dev/stdin", reject: true, expect: "/dev/stdin"},
		{name: "unix dev stdout", path: "/dev/stdout", reject: true, expect: "/dev/stdout"},
		{name: "unix dev stderr", path: "/dev/stderr", reject: true, expect: "/dev/stderr"},
		{name: "unix dev null refused", path: "/dev/null", reject: true, expect: "/dev/null"},
		{name: "unix dev fd child", path: "/dev/fd/3", reject: true, expect: "/dev/fd"},
		{name: "unix proc pid fd", path: "/proc/123/fd/2", reject: true, expect: "/proc/"},
		{name: "unix proc self fd", path: "/proc/self/fd/0", reject: true, expect: "/proc/"},
		{name: "unix windows style dev zero", path: `C:\dev\zero`, reject: false},

		// Ordinary paths.
		{name: "plain file", path: "report.txt", reject: false},
		{name: "relative source file", path: "src/main.go", reject: false},
		{name: "windows plain file", path: `C:\src\main.go`, reject: false},
		{name: "unix plain file", path: "/home/user/notes.md", reject: false},
		{name: "unix dev near miss", path: "/dev/zeroes", reject: false},
		{name: "unix dev fd near miss", path: "/dev/fdinfo", reject: false},
		{name: "relative dev zero near miss", path: "dev/zero", reject: false},
		{name: "tmp dev zero near miss", path: "/tmp/dev/zero", reject: false},
		{name: "proc status near miss", path: "/proc/self/status", reject: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := unsupportedPathNameReason(tc.path)
			if !tc.reject {
				require.Equal(t, "", reason, "path %q must stay usable", tc.path)
				return
			}
			require.NotEmpty(t, reason, "path %q must be refused", tc.path)
			if tc.expect != "" {
				require.Contains(t, reason, tc.expect)
			}
		})
	}

	// Case follows host habits: Unix device names are case-sensitive there.
	if runtime.GOOS != "windows" {
		require.Equal(t, "", unsupportedPathNameReason("/DEV/ZERO"))
	}
}

func TestPathGuardUnsupportedFileModeReason(t *testing.T) {
	require.Equal(t, "", unsupportedFileModeReason(os.FileMode(0)))
	require.Equal(t, "", unsupportedFileModeReason(os.FileMode(0o644)))
	require.Equal(t, "", unsupportedFileModeReason(os.FileMode(0o4755)))
	require.Equal(t, "", unsupportedFileModeReason(os.ModeDir|0o755))

	require.Equal(t, "named pipe", unsupportedFileModeReason(os.ModeNamedPipe))
	require.Equal(t, "device", unsupportedFileModeReason(os.ModeDevice))
	require.Equal(t, "character device", unsupportedFileModeReason(os.ModeCharDevice))
	require.Equal(t, "character device", unsupportedFileModeReason(os.ModeDevice|os.ModeCharDevice))
	require.Equal(t, "socket", unsupportedFileModeReason(os.ModeSocket))
	require.Equal(t, "irregular file", unsupportedFileModeReason(os.ModeIrregular))
}

// TestPathGuardRealFIFORejectedByMode creates a real FIFO on Unix and asserts
// the type layer refuses it, i.e. a caller checking the guard can never reach
// the blocking open. syscall.Mkfifo has no Windows definition and this change
// is limited to the four path_guard/path_spelling files (no build-tag file),
// so the FIFO is created with the mkfifo helper instead; the test skips when
// the helper is unavailable rather than adding a dependency.
func TestPathGuardRealFIFORejectedByMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO files cannot be created on Windows; syscall.Mkfifo is unavailable")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo helper not found; skipping the real FIFO check")
	}

	fifoPath := filepath.Join(t.TempDir(), "blocking.pipe")
	if out, err := exec.Command(mkfifo, fifoPath).CombinedOutput(); err != nil {
		t.Skipf("mkfifo failed: %v (%s)", err, string(out))
	}

	// The name layer does not know this name; the mode layer must catch it.
	require.Equal(t, "", unsupportedPathNameReason(fifoPath))

	info, err := os.Lstat(fifoPath)
	require.NoError(t, err)
	require.Equal(t, "named pipe", unsupportedFileModeReason(info.Mode()))
}
