package remotetools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// ensureJobsRoot creates the target-local job root and verifies that one small
// file can be written. An explicit or default jobs.root that cannot be created
// is a startup failure: remote jobs have no in-memory fallback.
func ensureJobsRoot(root string) error {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return fmt.Errorf("jobs.root is required for the remote shell job runtime")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return fmt.Errorf("resolve jobs.root %q: %w", trimmed, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("create jobs.root %q: %w", abs, err)
	}
	probe, err := os.CreateTemp(abs, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("jobs.root %q is not writable: %w", abs, err)
	}
	probePath := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("close jobs.root probe %q: %w", probePath, closeErr)
	}
	if removeErr := os.Remove(probePath); removeErr != nil {
		return fmt.Errorf("remove jobs.root probe %q: %w", probePath, removeErr)
	}
	return nil
}

// newRemoteBackgroundManager constructs the single target-side job runtime.
// It maps jobs.root to the core store/log directories instead of maintaining a
// second job implementation.
func newRemoteBackgroundManager(cfg *Config, sandbox *Sandbox) (*background.Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("remote config is required for jobs")
	}
	root := strings.TrimSpace(cfg.Jobs.Root)
	if err := ensureJobsRoot(root); err != nil {
		return nil, err
	}
	cleanupLegacyRemoteJobs(root)

	env := os.Environ()
	if sandbox != nil {
		env = sandbox.FilterEnv(env)
	}
	retention := cfg.Jobs.Retention.Duration
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	storePath := filepath.Join(root, "background.db")
	probeStore, err := background.NewSQLiteStore(&background.StoreConfig{Path: storePath})
	if err != nil {
		return nil, fmt.Errorf("open jobs store %q: %w", storePath, err)
	}
	if err := probeStore.Close(); err != nil {
		return nil, fmt.Errorf("close jobs store probe %q: %w", storePath, err)
	}
	maxOutput := cfg.Limits.CaptureLimit
	if maxOutput <= 0 {
		maxOutput = cfg.Limits.MaxOutputBytes
	}
	manager := background.NewManager(background.Config{
		StorePath:       storePath,
		LogDir:          filepath.Join(root, "logs"),
		MaxOutputBytes:  maxOutput,
		DefaultTimeout:  0,
		Retention:       retention,
		Shell:           cfg.ResolvedShell,
		Env:             env,
		DisableDetached: true,
		PrepareCommand: func(shell runtimeexecutor.Shell, command string, cmd *exec.Cmd) {
			configureRawCommandLine(cmd, shellRawCmdLine(shell, command))
		},
	})
	return manager, nil
}

// legacyJobRecord is the on-disk shape of the removed remotetools.JobManager.
// It exists only so startup can terminate leftovers and delete the old tree; it
// is never read by a runtime path.
type legacyJobRecord struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	State     string    `json:"state"`
}

// cleanupLegacyRemoteJobs removes the pre-core jobs/<id>/job.json layout. The
// old records have no reliable owner, so they are deliberately not migrated.
// Kill and cleanup failures are warnings; they must not resurrect the old job
// manager or stop the new one.
func cleanupLegacyRemoteJobs(root string) {
	legacyRoot := filepath.Join(root, "jobs")
	entries, err := os.ReadDir(legacyRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: legacy job cleanup: read %s: %v\n", legacyRoot, err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(legacyRoot, entry.Name())
		raw, readErr := os.ReadFile(filepath.Join(dir, "job.json"))
		if readErr == nil {
			var record legacyJobRecord
			if json.Unmarshal(raw, &record) == nil {
				terminateLegacyJob(record)
			}
		}
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: legacy job cleanup: remove %s: %v\n", dir, removeErr)
		}
	}
	_ = os.Remove(legacyRoot)
}

func terminateLegacyJob(record legacyJobRecord) {
	if record.PID <= 0 || record.PID == os.Getpid() || !strings.EqualFold(strings.TrimSpace(record.State), "running") {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(record.PID), "/T", "/F").Run()
		return
	}
	// Give a build a chance to flush state, then force it. SIGTERM/SIGKILL are
	// best-effort: a dead or reused PID is expected after a reboot.
	_ = exec.Command("kill", "-TERM", strconv.Itoa(record.PID)).Run()
	time.Sleep(200 * time.Millisecond)
	_ = exec.Command("kill", "-KILL", strconv.Itoa(record.PID)).Run()
}

// closeBackgroundManager centralizes the lifecycle used by Server.Close.
func closeBackgroundManager(manager *background.Manager) error {
	if manager == nil {
		return nil
	}
	return manager.Close()
}

// isUTF8Start reports whether b is a UTF-8 leading byte.
func isUTF8Start(b byte) bool {
	return b&0xC0 != 0x80
}
