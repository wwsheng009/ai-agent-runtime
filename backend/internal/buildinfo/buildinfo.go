package buildinfo

import (
	"runtime/debug"
	"strings"
)

var (
	version   string
	gitCommit string
	gitDirty  string
	buildTime string
)

// BackendProvenance identifies the backend binary currently serving requests.
type BackendProvenance struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	GitDirty  bool   `json:"git_dirty"`
	BuildTime string `json:"build_time"`
}

type vcsProvenance struct {
	version   string
	gitCommit string
	gitDirty  string
}

// Backend returns linker-injected build metadata with Go VCS metadata as a
// fallback for ordinary local builds.
func Backend() BackendProvenance {
	return resolveBackendProvenance(version, gitCommit, gitDirty, buildTime, readVCSProvenance())
}

func resolveBackendProvenance(
	versionOverride string,
	commitOverride string,
	dirtyOverride string,
	buildTimeOverride string,
	vcs vcsProvenance,
) BackendProvenance {
	resolvedVersion := firstKnown(versionOverride, vcs.version)
	if resolvedVersion == "" || resolvedVersion == "(devel)" {
		resolvedVersion = "dev"
	}

	resolvedCommit := firstKnown(commitOverride, vcs.gitCommit)
	if resolvedCommit == "" {
		resolvedCommit = "unknown"
	}

	resolvedBuildTime := strings.TrimSpace(buildTimeOverride)
	if resolvedBuildTime == "" {
		resolvedBuildTime = "unknown"
	}

	return BackendProvenance{
		Version:   resolvedVersion,
		GitCommit: resolvedCommit,
		GitDirty:  parseDirty(firstKnown(dirtyOverride, vcs.gitDirty)),
		BuildTime: resolvedBuildTime,
	}
}

func readVCSProvenance() vcsProvenance {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return vcsProvenance{}
	}

	result := vcsProvenance{version: strings.TrimSpace(info.Main.Version)}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			result.gitCommit = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			result.gitDirty = strings.TrimSpace(setting.Value)
		}
	}
	return result
}

func firstKnown(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" && !strings.EqualFold(trimmed, "unknown") {
			return trimmed
		}
	}
	return ""
}

func parseDirty(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "dirty", "modified":
		return true
	default:
		return false
	}
}
