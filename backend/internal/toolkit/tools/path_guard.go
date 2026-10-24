package tools

import (
	"errors"
	"os"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/pathguard"
)

// The special-path refusal rules themselves live in toolcore/pathguard so every
// surface (and every core handler) refuses the same names with the same wording.
// This file keeps the local names and the local error vocabulary.

func unsupportedPathNameReason(path string) string {
	return pathguard.NameReason(path)
}

func unsupportedFileModeReason(mode os.FileMode) string {
	return pathguard.ModeReason(mode)
}

// errDevicePath marks a refusal of a device/stream path by the file tools. It
// is wrapped into a runtime error so the deny carries a machine-readable code
// and metadata (policy/failure_class/next_action) instead of a bare message.
var errDevicePath = errors.New("device or stream path is not a regular file")

// devicePathRefusalError turns a special-path refusal into a structured deny.
// The message matches the view tool's long-standing wording so every file tool
// refuses the same path the same way; the metadata gives the recovery route.
func devicePathRefusalError(targetPath, reason string) error {
	return runtimeerrors.WrapWithContext(
		runtimeerrors.ErrToolInvalidArgs,
		pathguard.RefusalMessage(targetPath, reason),
		errDevicePath,
		map[string]interface{}{
			"policy":         "device_path",
			"failure_class":  "device_path",
			"target_path":    targetPath,
			"refusal_reason": reason,
			"path_refused":   true,
			"next_action":    pathguard.RefusalNextAction,
		},
	)
}
