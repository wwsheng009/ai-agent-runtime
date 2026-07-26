package plugins

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// InstallOptions controls local plugin install (copy into plugins home).
type InstallOptions struct {
	// Source is a plugin directory containing plugin.yaml.
	Source string
	// TargetRoot is the plugins home (defaults to DefaultPluginsHome()).
	TargetRoot string
	// Name overrides manifest name for the install directory.
	Name string
	// Force overwrites an existing install directory.
	Force bool
	// DryRun reports the plan without writing.
	DryRun bool
	// Trust sets initial trust (default untrusted).
	Trust TrustLevel
	// State records install in the trust store (optional; created at default when nil and !DryRun).
	State *StateStore
	// SkipState skips trust store update.
	SkipState bool
}

// InstallResult describes a completed or planned install.
type InstallResult struct {
	Name             string     `json:"name"`
	Source           string     `json:"source"`
	TargetRoot       string     `json:"target_root"`
	TargetDir        string     `json:"target_dir"`
	Installed        bool       `json:"installed"`
	DryRun           bool       `json:"dry_run"`
	AlreadyInstalled bool       `json:"already_installed"`
	Overwritten      bool       `json:"overwritten"`
	Trust            TrustLevel `json:"trust"`
	FileCount        int        `json:"file_count"`
	DirCount         int        `json:"dir_count"`
	Message          string     `json:"message"`
}

// Install copies a plugin package into the local plugins home and records state.
func Install(opts InstallOptions) (InstallResult, error) {
	result := InstallResult{
		DryRun: opts.DryRun,
		Trust:  normalizeTrust(opts.Trust),
	}
	source := filepath.Clean(strings.TrimSpace(opts.Source))
	if source == "" {
		return result, fmt.Errorf("plugin source is required")
	}
	absSource, err := filepath.Abs(source)
	if err == nil {
		source = absSource
	}
	result.Source = source

	manifest, _, err := LoadManifest(source)
	if err != nil {
		return result, err
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = manifest.Name
	}
	if err := validatePluginName(name); err != nil {
		return result, err
	}
	result.Name = name

	targetRoot := strings.TrimSpace(opts.TargetRoot)
	if targetRoot == "" {
		targetRoot = DefaultPluginsHome()
	}
	if abs, err := filepath.Abs(targetRoot); err == nil {
		targetRoot = abs
	}
	result.TargetRoot = targetRoot
	targetDir := filepath.Join(targetRoot, name)
	result.TargetDir = targetDir

	if same, err := samePath(source, targetDir); err == nil && same {
		return result, fmt.Errorf("source and target are the same path: %s", targetDir)
	}

	info, err := os.Stat(targetDir)
	exists := err == nil && info.IsDir()
	if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	if exists && !opts.Force {
		result.AlreadyInstalled = true
		return result, fmt.Errorf("plugin already installed at %s (use --force to overwrite)", targetDir)
	}
	if exists && opts.Force {
		result.Overwritten = true
	}

	fileCount, dirCount, err := countTree(source)
	if err != nil {
		return result, err
	}
	result.FileCount = fileCount
	result.DirCount = dirCount

	if opts.DryRun {
		result.Message = "plugin install dry-run"
		return result, nil
	}

	if exists {
		if err := os.RemoveAll(targetDir); err != nil {
			return result, fmt.Errorf("remove existing plugin: %w", err)
		}
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return result, err
	}
	if err := copyDir(source, targetDir); err != nil {
		_ = os.RemoveAll(targetDir)
		return result, fmt.Errorf("copy plugin: %w", err)
	}

	if !opts.SkipState {
		store := opts.State
		if store == nil {
			store = NewStateStore(filepath.Join(targetRoot, StateFileName))
		}
		if _, err := store.UpsertInstall(name, targetDir, result.Trust); err != nil {
			return result, fmt.Errorf("record plugin state: %w", err)
		}
	}

	result.Installed = true
	result.Message = "plugin installed"
	return result, nil
}

func samePath(left, right string) (bool, error) {
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false, err
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false, err
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs), nil
}

func countTree(root string) (files, dirs int, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs++
			return nil
		}
		files++
		return nil
	})
	return files, dirs, err
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
