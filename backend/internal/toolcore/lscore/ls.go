// Package lscore implements the shared directory listing used by the local and
// remote ls surfaces: one depth-limited tree walk, one renderer, one schema.
package lscore

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

const (
	// DefaultLimit is the one-level page size.
	DefaultLimit = 200
	// DefaultTreeLimit bounds a depth>1 listing.
	DefaultTreeLimit = 1000
	// MaxDepth is the deepest tree the tool will walk. Deeper requests are
	// clamped rather than rejected: the caller asked for "as deep as sensible".
	MaxDepth = 10
	// OutputBudgetBytes is ls's model-visible window.
	OutputBudgetBytes = 16 * 1024
)

// TreeEntry is one path in a depth-limited recursive listing.
type TreeEntry struct {
	RelPath string
	Depth   int
	Dir     bool
}

// TreeListing is the shared recursive listing shape used by the builtin ls
// tool. Total counts the retained window, matching the existing ls metadata
// contract when a limit or byte budget truncates the render.
type TreeListing struct {
	Entries   []TreeEntry
	Total     int
	Dirs      int
	Files     int
	Truncated bool
	// LimitHit reports a row cut, the local tool's `limit_hit`. Truncated also
	// covers the byte window, which is not a row limit.
	LimitHit bool
}

// ReadTree walks path up to maxDepth, sorts paths deterministically, and keeps
// at most limit entries. Directory entries at the depth boundary are skipped
// without descending, so callers can preserve the long-standing ls contract.
func ReadTree(path string, maxDepth, limit int) (TreeListing, error) {
	return ReadTreeVia(context.Background(), localio.Access, path, maxDepth, limit)
}

// ReadTreeVia walks one tree through the I/O seam: the same listing with the
// filesystem decision taken out of the walk, which is what lets a remote surface
// share this renderer instead of keeping its own copy of ls.
//
// The traversal is specified by what filepath.WalkDir did, not by what is
// convenient to write here, because the limit truncates *during* collection: which
// entries were already gathered when the cut landed decides which rows survive, so
// a listing that is merely "as deep as before" would still be a different answer.
// Therefore:
//
//   - one directory read per directory, which is WalkDir's own cost shape;
//   - lexical order per directory (ReadDir is sorted), depth-first, a directory
//     emitted before its contents;
//   - the root is Lstat-ed first, exactly as filepath.WalkDir did: a file as the
//     root yields an empty listing with no error, a missing root yields the error
//     the handler turns into NOT_FOUND, and a symlinked root is NOT followed -
//     following it would let a listing climb out of the authorized tree;
//   - a directory that cannot be read is left undescended rather than failing the
//     listing, because one off-limits subdirectory is a hole in the tree, not an
//     error in the answer;
//   - the depth boundary drops the entries sitting on it and does not descend them.
func ReadTreeVia(ctx context.Context, access toolcore.Access, path string, maxDepth, limit int) (TreeListing, error) {
	if access == nil {
		access = localio.Access
	}
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > MaxDepth {
		maxDepth = MaxDepth
	}
	if limit < 1 {
		limit = DefaultTreeLimit
	}

	listing := TreeListing{}
	rootInfo, statErr := access.Lstat(ctx, path)
	if statErr != nil {
		return TreeListing{}, statErr
	}
	if !rootInfo.IsDir() {
		// WalkDir visited a non-directory root once and dropped it (its relative path
		// was "."): an empty listing, not an error. A symlink to a directory lands
		// here too, which is what keeps ls from descending through a link that points
		// outside the authorized roots.
		return listing, nil
	}

	// stop is the SkipAll equivalent: once the limit has landed the walk collects
	// nothing further, in either recursion frame.
	stop := false
	var descend func(dirPath, relPrefix string, depth int)
	descend = func(dirPath, relPrefix string, depth int) {
		entries, err := access.ReadDir(ctx, dirPath)
		if err != nil {
			// A directory whose contents cannot be read is a hole in the tree.
			// Only the root's own error is allowed to abort the call.
			return
		}
		for _, entry := range entries {
			if stop {
				return
			}
			name := entry.Name()
			relPath := name
			if relPrefix != "" {
				relPath = relPrefix + "/" + name
			}
			if depth >= maxDepth {
				// The entry sitting on the boundary is dropped, and a directory is
				// not descended: exactly the old SkipDir branch.
				continue
			}
			item := TreeEntry{RelPath: relPath, Depth: depth, Dir: entry.IsDir()}
			listing.Entries = append(listing.Entries, item)
			if item.Dir {
				listing.Dirs++
			} else {
				listing.Files++
			}
			// The limit is checked before descending, and that ordering is the
			// contract, not a micro-optimisation: filepath.WalkDir stopped as soon as
			// the count reached the limit, so a directory that *filled* the limit was
			// never entered. Checking after descending collects that directory's
			// children instead, which changes which rows survive a cut - a difference
			// only a differential over many depth/limit pairs can see (it bit exactly
			// this function on its first draft).
			if len(listing.Entries) >= limit {
				listing.Truncated = true
				listing.LimitHit = true
				stop = true
				return
			}
			if item.Dir {
				descend(filepath.Join(dirPath, name), relPath, depth+1)
			}
		}
	}
	descend(path, "", 0)
	sort.Slice(listing.Entries, func(i, j int) bool {
		if listing.Entries[i].RelPath == listing.Entries[j].RelPath {
			if listing.Entries[i].Dir == listing.Entries[j].Dir {
				return listing.Entries[i].Depth < listing.Entries[j].Depth
			}
			return listing.Entries[i].Dir && !listing.Entries[j].Dir
		}
		return listing.Entries[i].RelPath < listing.Entries[j].RelPath
	})
	listing.Total = len(listing.Entries)
	return listing, nil
}

// RenderTree writes one depth-limited listing in the local tool's shape: a
// "目录: <path>" header, indented entries with a directory/file marker, a
// statistics trailer, and a truncation notice that names the next step.
//
// The byte window is applied here, not after rendering: the header and trailer
// are reserved first, so a cut listing still ends with a complete trailer and a
// usable recovery instruction.
func RenderTree(display string, listing TreeListing, budget int) (string, int, bool) {
	if display == "" {
		display = "."
	}
	header := fmt.Sprintf("目录: %s\n\n", display)
	trailer := treeTrailer(listing.Files, listing.Dirs, listing.Total)
	listingBudget := budget - len(header) - len(trailer)
	if budget <= 0 || listingBudget < 0 {
		listingBudget = 0
	}

	var builder strings.Builder
	builder.WriteString(header)
	rendered := 0
	used := 0
	byteBudgetHit := false
	if len(listing.Entries) == 0 {
		builder.WriteString("(空目录)")
	} else {
		for _, entry := range listing.Entries {
			prefix := strings.Repeat("  ", entry.Depth)
			line := fmt.Sprintf("%s📄 %s\n", prefix, entry.RelPath)
			if entry.Dir {
				line = fmt.Sprintf("%s📁 %s/\n", prefix, entry.RelPath)
			}
			if rendered > 0 && used+len(line) > listingBudget {
				byteBudgetHit = true
				break
			}
			used += len(line)
			rendered++
			builder.WriteString(line)
		}
	}
	builder.WriteString(fmt.Sprintf("\n统计: %d 个文件, %d 个目录", listing.Files, listing.Dirs))
	if listing.Truncated || byteBudgetHit {
		// ls has no offset paging: the recovery path is a smaller depth or a
		// narrower directory. Saying only "N shown" reads as data loss.
		fmt.Fprintf(&builder,
			"\n(条目过多，已显示前 %d 个，共 %d 个；next_step: 减小 depth 或分目录查看，不要原样重试)",
			rendered, listing.Total)
	}
	return builder.String(), rendered, byteBudgetHit
}

// treeTrailer is the worst-case length of the lines appended after the entry
// list (statistics + truncation notice). Both notices print counts bounded by
// the total, so the reserve is an upper bound and not an estimate.
func treeTrailer(files, dirs, total int) string {
	return fmt.Sprintf("\n统计: %d 个文件, %d 个目录", files, dirs) +
		fmt.Sprintf("\n(条目过多，已显示前 %d 个，共 %d 个；next_step: 减小 depth 或分目录查看，不要原样重试)", total, total)
}
