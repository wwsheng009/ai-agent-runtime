package aiclipaths

const (
	// BuildProfile identifies the compile-time defaults selected by build tags.
	BuildProfile = buildProfile

	// DefaultConfigFileName is the bootstrap config filename used under .aicli
	// and configs directories when the user did not pass an explicit path.
	DefaultConfigFileName = defaultConfigFileName

	// DefaultCLIConfigFileName is the standalone config filename searched in the
	// current directory by aicli.
	DefaultCLIConfigFileName = defaultCLIConfigFileName

	// StandardConfigFileName is the main build profile's bootstrap config
	// filename. It is kept constant across build profiles so non-main binaries
	// (for example win7compat) can fall back to standard-layout agent configs
	// (config.yaml) when no profile-specific file exists.
	StandardConfigFileName = "config.yaml"

	// DefaultRuntimeConfigFileName is the packaged runtime config selected by the
	// active build profile.
	DefaultRuntimeConfigFileName = defaultRuntimeConfigFileName

	// DefaultRuntimeConfigRelativePath uses forward slashes so generated YAML is
	// portable across operating systems.
	DefaultRuntimeConfigRelativePath = "configs/" + DefaultRuntimeConfigFileName

	// DefaultSessionHistoryFileName is only the safety fallback used when an
	// effective runtime config omits sessions.storePath. Explicit YAML always
	// wins; the Win7 fallback remains separate so legacy and current SQLite
	// drivers never share WAL/SHM sidecars even when a package asset is missing.
	DefaultSessionHistoryFileName = defaultSessionHistoryFileName
)
