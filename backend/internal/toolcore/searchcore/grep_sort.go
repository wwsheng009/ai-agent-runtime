package searchcore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GrepSortKeys lists the accepted --sort/--sortr values. The set is ripgrep's
// own, because one tool must not spell the same ordering two ways.
var GrepSortKeys = []string{"none", "path", "modified", "accessed", "created"}

// NormalizeGrepSortKey folds a caller's spelling onto the shared vocabulary.
// The empty string means "not requested"; it is not the same as "none" (which
// explicitly asks for an unspecified order).
func NormalizeGrepSortKey(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	for _, key := range GrepSortKeys {
		if value == key {
			return value, nil
		}
	}
	return "", fmt.Errorf("sort/sortr 参数无效: %q（仅支持 %s）", raw, strings.Join(GrepSortKeys, "/"))
}

// grepSortKeySupported reports whether the walker can order by this key on the
// current host. Time keys need metadata the OS may not expose; on such a host
// the engine gets the first turn (RequiresRipgrep), and if it is unavailable
// the walker reports the missing capability instead of silently changing order.
func grepSortKeySupported(key string) bool {
	switch strings.TrimSpace(key) {
	case "", "none", "path", "modified":
		return true
	case "accessed", "created":
		return grepFileTimesSupported()
	default:
		return false
	}
}

// grepSortCandidate is one file discovered by the walk, kept together with the
// root scope it belongs to so the later scan renders and pages exactly as the
// streaming path would.
type grepSortCandidate struct {
	root  string
	rules *grepIgnoreRules
	path  string
}

// sortGrepCandidates orders one root's candidates before scanning. Sorting
// happens per root, matching the engine's one-process-per-root contract: root
// order stays the caller's order, and only the files inside a root move.
//
// Ties are broken by the root-relative path (ascending in both directions) so
// the order is total and deterministic even when timestamps collide.
func sortGrepCandidates(candidates []grepSortCandidate, key string, reverse bool) ([]grepSortCandidate, error) {
	key = strings.TrimSpace(key)
	if key == "" || key == "none" {
		return candidates, nil
	}
	if !grepSortKeySupported(key) {
		return nil, fmt.Errorf("sort=%s 在当前平台不可用：该主机无法读取对应的文件时间", key)
	}

	type entry struct {
		candidate grepSortCandidate
		rel       string
		primary   int64
		hasTime   bool
	}
	entries := make([]entry, 0, len(candidates))
	for _, candidate := range candidates {
		rel := filepath.ToSlash(relativeGrepCandidate(candidate.root, candidate.path))
		item := entry{candidate: candidate, rel: rel}
		if key != "path" {
			info, err := os.Stat(candidate.path)
			if err != nil {
				// The file vanished between discovery and sorting; the scan
				// would have skipped it too.
				continue
			}
			var stamp time.Time
			switch key {
			case "modified":
				stamp = info.ModTime()
			case "accessed":
				accessed, ok := fileAccessTime(info)
				if !ok {
					return nil, fmt.Errorf("sort=%s 在当前平台不可用：无法读取访问时间", key)
				}
				stamp = accessed
			case "created":
				created, ok := fileCreationTime(info)
				if !ok {
					return nil, fmt.Errorf("sort=%s 在当前平台不可用：无法读取创建时间", key)
				}
				stamp = created
			}
			item.primary = stamp.UnixNano()
			item.hasTime = true
		}
		entries = append(entries, item)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if key == "path" {
			if left.rel != right.rel {
				if reverse {
					return left.rel > right.rel
				}
				return left.rel < right.rel
			}
			return left.candidate.path < right.candidate.path
		}
		if left.hasTime && right.hasTime && left.primary != right.primary {
			if reverse {
				return left.primary > right.primary
			}
			return left.primary < right.primary
		}
		return left.rel < right.rel
	})

	ordered := make([]grepSortCandidate, 0, len(entries))
	for _, item := range entries {
		ordered = append(ordered, item.candidate)
	}
	return ordered, nil
}
