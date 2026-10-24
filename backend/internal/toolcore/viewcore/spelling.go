package viewcore

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/pathrepair"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// HealSpelling repairs a request whose name differs from the on-disk name only
// in case or invisible Unicode code points (narrow no-break space, curly
// quotes): an existence check fails forever and the model retries the same dead
// path (analysis §3.7).
//
// It returns the unique candidate to use, or the candidate list when the repair
// is ambiguous. Every candidate is re-checked against the surface boundary: a
// repair must never become an escape hatch, so the checker that admits the
// original request also has to admit the candidate. The folding itself lives in
// internal/pathrepair, the same notion of "same name" the tool preflight layer
// ranks candidates with.
func HealSpelling(checker toolcore.PermissionChecker, absPath string) (healed string, candidates []string) {
	dir, base := filepath.Dir(absPath), filepath.Base(absPath)
	matches := pathrepair.FindSpellingMatches(dir, base)
	if len(matches) == 0 {
		return "", nil
	}
	candidates = make([]string, 0, len(matches))
	for _, name := range matches {
		candidate := filepath.Join(dir, name)
		if checker != nil {
			if err := checker.CheckPath(toolcore.OpRead, candidate); err != nil {
				continue
			}
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 1 {
		return candidates[0], candidates
	}
	return "", candidates
}

// AmbiguousSpellingResult is the "pick one" answer when several names differ
// from the request only by invisible code points: the model gets the candidates
// instead of retrying a path that can never exist.
func AmbiguousSpellingResult(displayPath, absPath string, candidates []string) *toolcore.Result {
	message := fmt.Sprintf(
		"路径不存在: %s；父目录中存在多个仅大小写/Unicode 拼写不同的候选: %s。请确认后重试。",
		displayPath, strings.Join(candidates, ", "),
	)
	return &toolcore.Result{
		Success:    false,
		OutputKind: toolcore.KindText,
		Content:    message,
		Err:        errors.New(message),
		ErrorCode:  toolcore.ErrNotFound,
		NextAction: "从 metadata.path_candidates 里确认一个真实存在的路径后重试，不要原样重试同一个请求。",
		Metadata: map[string]any{
			"file_path":       absPath,
			"path_candidates": candidates,
			"path_auto_heal":  "ambiguous",
		},
	}
}
