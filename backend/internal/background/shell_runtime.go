package background

import (
	"context"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// resolveJobShellRuntime captures the shell environment at job submission.
//
// A process-wide manager serves jobs from many workspaces. Resolving at
// execution time would let a later workspace/settings change rewrite an
// already-submitted job's dialect. The captured runtime is stored on the
// managed job and persisted for recovery.
func resolveJobShellRuntime(ctx context.Context, override runtimeexecutor.Shell) *runtimeexecutor.ShellRuntime {
	if runtime := toolctx.ShellRuntimeFromContext(ctx); runtime != nil {
		return runtime
	}
	if strings.TrimSpace(override.Path) != "" {
		return &runtimeexecutor.ShellRuntime{
			Shell:      override,
			Delivery:   runtimeexecutor.CurrentScriptDelivery(),
			SourcePath: "background.config.shell",
		}
	}
	runtime, err := runtimeexecutor.CurrentShellRuntime()
	if runtime != nil {
		runtime.ResolveErr = err
	}
	return runtime
}

// applyShellRuntimeMetadata persists the identity needed to execute/recover a
// job without re-probing the current global config. shell.env is intentionally
// not persisted (it may contain secrets); the runtime.yaml path lets recovery
// reload it when still available.
func applyShellRuntimeMetadata(metadata map[string]interface{}, runtime *runtimeexecutor.ShellRuntime) {
	if metadata == nil || runtime == nil {
		return
	}
	if path := strings.TrimSpace(runtime.Shell.Path); path != "" {
		metadata["shell_path"] = path
	}
	if shellType := strings.TrimSpace(string(runtime.Shell.Type)); shellType != "" {
		metadata["shell_type"] = shellType
	}
	if id := strings.TrimSpace(runtime.Shell.ID); id != "" {
		metadata["shell_id"] = id
	}
	if dialect := strings.TrimSpace(string(runtime.Shell.Dialect)); dialect != "" {
		metadata["shell_dialect"] = dialect
	}
	if delivery := strings.TrimSpace(string(runtime.EffectiveDelivery())); delivery != "" {
		metadata["shell_delivery"] = delivery
	}
	if source := strings.TrimSpace(runtime.SourcePath); source != "" {
		metadata["shell_config_path"] = source
	}
	if fingerprint := runtime.RuntimeFingerprint(); fingerprint != "" {
		metadata["shell_fingerprint"] = fingerprint
	}
}

// shellRuntimeFromJobMetadata reconstructs the captured identity for recovered
// jobs. shell.env is not persisted; when it matters the job is re-run from its
// original workspace/runtime config, not from a later global default.
func shellRuntimeFromJobMetadata(job Job) *runtimeexecutor.ShellRuntime {
	path, _ := stringMetadataValue(job.Metadata, "shell_path")
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	shellType, _ := stringMetadataValue(job.Metadata, "shell_type")
	shellID, _ := stringMetadataValue(job.Metadata, "shell_id")
	dialect, _ := stringMetadataValue(job.Metadata, "shell_dialect")
	delivery, _ := stringMetadataValue(job.Metadata, "shell_delivery")
	source, _ := stringMetadataValue(job.Metadata, "shell_config_path")
	runtime := &runtimeexecutor.ShellRuntime{
		Shell: runtimeexecutor.Shell{
			Path:    path,
			Type:    runtimeexecutor.ShellType(strings.TrimSpace(shellType)),
			ID:      strings.TrimSpace(shellID),
			Dialect: runtimeexecutor.ShellDialect(strings.TrimSpace(dialect)),
		},
		Delivery:   runtimeexecutor.ScriptDelivery(strings.TrimSpace(delivery)),
		SourcePath: strings.TrimSpace(source),
	}
	runtime.Fingerprint = runtime.RuntimeFingerprint()
	return runtime
}
