package executor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// bubblewrapBinary is the expected executable name for the Linux bwrap backend.
const bubblewrapBinary = "bwrap"

// bwrapBind describes a filesystem bind mount for bubblewrap planning.
type bwrapBind struct {
	HostPath string
	ReadOnly bool
}

// planBubblewrapArgs builds a bubblewrap argv that wraps command/args under
// application-layer path + network policy. Pure function for unit testing on
// any GOOS; the Linux backend decides availability via LookPath.
//
// Minimal isolation model (MVP):
//   - die-with-parent
//   - unshare user/pid/ipc/uts (best-effort process namespace isolation)
//   - unshare net when BlockNetwork
//   - ro-bind common host roots needed to run tools (/usr /bin /lib* /etc)
//   - bind AllowedPaths (RW unless also listed in ReadOnlyPaths)
//   - ro-bind ReadOnlyPaths
//   - tmpfs /tmp, dev, proc
//   - chdir workdir when set
//
// This is intentionally thin — not a full remote workspace daemon / rootfs image.
func planBubblewrapArgs(req OSSandboxRequest) (command string, args []string, err error) {
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		return "", nil, fmt.Errorf("command cannot be empty")
	}

	cfg := req.Config
	binds, err := planBwrapBinds(cfg, req.WorkDir)
	if err != nil {
		return "", nil, err
	}

	out := make([]string, 0, 48+len(binds)*3+len(req.Args))
	out = append(out,
		"--die-with-parent",
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
	)
	if cfg.BlockNetwork {
		out = append(out, "--unshare-net")
	}

	// Host roots required to execute typical CLI tools. Read-only by design.
	for _, root := range defaultBwrapHostRoots() {
		out = append(out, "--ro-bind-try", root, root)
	}
	out = append(out,
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	)

	for _, bind := range binds {
		flag := "--bind"
		if bind.ReadOnly {
			flag = "--ro-bind"
		}
		out = append(out, flag, bind.HostPath, bind.HostPath)
	}

	workDir := strings.TrimSpace(req.WorkDir)
	if workDir != "" {
		abs, absErr := cleanAbsPath(workDir)
		if absErr != nil {
			return "", nil, fmt.Errorf("workdir: %w", absErr)
		}
		out = append(out, "--chdir", abs)
	}

	// End of bwrap options; remaining tokens are the guest command.
	out = append(out, "--", cmd)
	out = append(out, req.Args...)
	return bubblewrapBinary, out, nil
}

func planBwrapBinds(cfg SandboxConfig, workDir string) ([]bwrapBind, error) {
	// Map path -> readOnly. Read-only wins when a path appears in both lists.
	type pathMode struct {
		readOnly bool
	}
	modes := map[string]pathMode{}

	add := func(raw string, readOnly bool) error {
		abs, err := cleanAbsPath(raw)
		if err != nil {
			return err
		}
		// Skip filesystem roots as explicit binds; they are covered by host roots.
		if abs == string(filepath.Separator) || abs == filepath.VolumeName(abs)+string(filepath.Separator) {
			return nil
		}
		if existing, ok := modes[abs]; ok {
			modes[abs] = pathMode{readOnly: existing.readOnly || readOnly}
			return nil
		}
		modes[abs] = pathMode{readOnly: readOnly}
		return nil
	}

	for _, p := range cfg.AllowedPaths {
		if err := add(p, false); err != nil {
			return nil, fmt.Errorf("allowed path: %w", err)
		}
	}
	for _, p := range cfg.ReadOnlyPaths {
		if err := add(p, true); err != nil {
			return nil, fmt.Errorf("read-only path: %w", err)
		}
	}
	if strings.TrimSpace(workDir) != "" {
		// Ensure workdir is reachable inside the sandbox even if not listed.
		if err := add(workDir, false); err != nil {
			return nil, fmt.Errorf("workdir bind: %w", err)
		}
		// If workdir falls under a read-only allow, keep read-only.
		if abs, err := cleanAbsPath(workDir); err == nil {
			for _, ro := range cfg.ReadOnlyPaths {
				roAbs, roErr := cleanAbsPath(ro)
				if roErr == nil && pathWithinBase(abs, roAbs) {
					modes[abs] = pathMode{readOnly: true}
					break
				}
			}
		}
	}

	if len(modes) == 0 {
		return nil, nil
	}

	paths := make([]string, 0, len(modes))
	for p := range modes {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]bwrapBind, 0, len(paths))
	for _, p := range paths {
		out = append(out, bwrapBind{HostPath: p, ReadOnly: modes[p].readOnly})
	}
	return out, nil
}

func defaultBwrapHostRoots() []string {
	// Keep conservative and portable across common Linux layouts.
	// --ro-bind-try ignores missing paths at runtime.
	return []string{
		"/usr",
		"/bin",
		"/sbin",
		"/lib",
		"/lib64",
		"/lib32",
		"/etc",
		"/opt",
		"/home",
		// busybox / alpine sometimes need /var/run style paths via /run
		"/run",
	}
}

// formatBwrapPlan is a test/debug helper that renders planned argv.
func formatBwrapPlan(command string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, command)
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
