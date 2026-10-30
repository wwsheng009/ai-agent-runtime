package searchcore

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// seamWalkFunc is fs.WalkDirFunc under the name this package uses for the seam.
// os.DirEntry is an alias of fs.DirEntry, so every existing callback keeps compiling
// unchanged.
type seamWalkFunc func(path string, entry fs.DirEntry, err error) error

// walkViaAccess is filepath.WalkDir over an accessor, reproduced against the standard
// library's own text rather than from memory, because traversal order is not a cosmetic
// property in this package: paging, the fetch limit and the early `SkipAll` are all
// decided in visit order, so a re-ordered walk silently changes which files a caller
// sees (ls learned that the hard way).
//
// The four rules that make it a copy and not an approximation:
//
//   - the root is Lstat-ed, never Stat-ed: a listing that follows a directory symlink
//     out of the authorized tree turns the jail into a suggestion;
//   - each directory is read once, and the entries are sorted by name, because
//     os.ReadDir sorted them and the walk is lexical;
//   - a ReadDir failure re-reports the same path to the callback with the error, exactly
//     like the stdlib's "second call", so a permission problem on one subtree costs that
//     subtree only;
//   - a child returning SkipDir makes the parent stop on the *remaining siblings*
//     (filepath's loop breaks), which is the documented rule for a non-directory entry,
//     and SkipDir on a directory only drops its contents.
//
// filepath.SkipAll still aborts the whole walk, and the top level still swallows both
// sentinels.
func walkViaAccess(ctx context.Context, access toolcore.Access, root string, onEntry seamWalkFunc) error {
	info, err := access.Lstat(ctx, root)
	if err != nil {
		err = onEntry(root, nil, err)
	} else {
		err = walkViaAccessEntry(ctx, access, root, fs.FileInfoToDirEntry(info), onEntry)
	}
	if err == filepath.SkipDir || err == filepath.SkipAll {
		return nil
	}
	return err
}

func walkViaAccessEntry(ctx context.Context, access toolcore.Access, path string, entry fs.DirEntry, onEntry seamWalkFunc) error {
	if err := onEntry(path, entry, nil); err != nil || !entry.IsDir() {
		if err == filepath.SkipDir && entry.IsDir() {
			err = nil
		}
		return err
	}

	entries, err := access.ReadDir(ctx, path)
	if err != nil {
		// Second call, to report the ReadDir error for this directory.
		err = onEntry(path, entry, err)
		if err != nil {
			if err == filepath.SkipDir && entry.IsDir() {
				err = nil
			}
			return err
		}
	}

	// The contract does not promise an order, and the local implementation's order is
	// only "sorted" because os.ReadDir says so. Sorting here is what keeps a remote
	// listing and a host listing paging to the same page.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, child := range entries {
		if err := walkViaAccessEntry(ctx, access, filepath.Join(path, child.Name()), child, onEntry); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}
