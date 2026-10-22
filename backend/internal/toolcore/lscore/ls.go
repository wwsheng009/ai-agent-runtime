// Package lscore implements the shared one-level directory listing used by
// local and remote ls surfaces.
package lscore

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const DefaultLimit = 200

type Entry struct {
	Name string
	Dir  bool
	Size int64
}

type Listing struct {
	Path      string
	Entries   []Entry
	Total     int
	Dirs      int
	Files     int
	Returned  int
	Truncated bool
}

// Read lists one directory, sorts directories first, and applies limit. Counts
// describe the returned window, matching the long-standing ls contract.
func Read(path string, limit int) (Listing, error) {
	if limit < 1 {
		limit = DefaultLimit
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Listing{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	listing := Listing{Path: path, Total: len(entries)}
	for _, entry := range entries {
		if listing.Returned >= limit {
			break
		}
		item := Entry{Name: entry.Name(), Dir: entry.IsDir(), Size: -1}
		if !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				item.Size = info.Size()
			}
		}
		listing.Entries = append(listing.Entries, item)
		if item.Dir {
			listing.Dirs++
		} else {
			listing.Files++
		}
		listing.Returned++
	}
	listing.Truncated = len(entries) > listing.Returned
	return listing, nil
}

// Render formats one listing with the stable type/size columns.
func Render(display string, listing Listing) string {
	if display == "" {
		display = "."
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== %s ===\n", display)
	for _, entry := range listing.Entries {
		if entry.Dir {
			fmt.Fprintf(&builder, "d  %s/\n", entry.Name)
			continue
		}
		switch {
		case entry.Size < 0:
			fmt.Fprintf(&builder, "f  %s\n", entry.Name)
		default:
			fmt.Fprintf(&builder, "f  %-48s %10d\n", entry.Name, entry.Size)
		}
	}
	if len(listing.Entries) == 0 {
		builder.WriteString("(empty directory)\n")
	}
	fmt.Fprintf(&builder, "\n[entries=%d dirs=%d files=%d]\n", listing.Total, listing.Dirs, listing.Files)
	if listing.Truncated {
		fmt.Fprintf(&builder, "[list truncated: showing %d of %d; raise limit or list a subdirectory]\n", listing.Returned, listing.Total)
	}
	return builder.String()
}

func Metadata(display string, listing Listing) map[string]interface{} {
	return map[string]interface{}{
		"path":         display,
		"entries":      listing.Total,
		"dirs":         listing.Dirs,
		"files":        listing.Files,
		"returned":     listing.Returned,
		"is_truncated": listing.Truncated,
	}
}
