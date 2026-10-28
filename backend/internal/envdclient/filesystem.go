package envdclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// filesystemService is the envd service name for filesystem operations.
const filesystemService = "filesystem.Filesystem"

// FileType is envd's entry kind discriminator.
type FileType string

// File types reported by envd.
const (
	FileTypeUnspecified FileType = "FILE_TYPE_UNSPECIFIED"
	FileTypeFile        FileType = "FILE_TYPE_FILE"
	FileTypeDirectory   FileType = "FILE_TYPE_DIRECTORY"
	FileTypeSymlink     FileType = "FILE_TYPE_SYMLINK"
)

// Entry is one filesystem entry.
//
// Two wire details are worth stating because both are easy to get wrong:
//
//   - Size arrives as a *string*, not a number ("size":"4096"). A plain int64
//     field would fail to decode.
//   - Mode arrives as a number (493 == 0755), while Permissions arrives as the
//     rendered "drwxr-xr-x" string.
type Entry struct {
	Name          string   `json:"name"`
	Type          FileType `json:"type"`
	Path          string   `json:"path"`
	Size          string   `json:"size"`
	Mode          uint32   `json:"mode"`
	Permissions   string   `json:"permissions"`
	Owner         string   `json:"owner"`
	Group         string   `json:"group"`
	ModifiedTime  string   `json:"modifiedTime"`
	SymlinkTarget string   `json:"symlinkTarget"`
}

// UnmarshalJSON accepts Size as either a JSON string or a JSON number.
//
// envd sends a string today, but a numeric form appears in some tooling, and a
// decode failure here would look like a corrupt directory listing rather than a
// schema difference. Accepting both keeps the failure mode honest.
func (e *Entry) UnmarshalJSON(payload []byte) error {
	type entryAlias Entry
	var raw struct {
		entryAlias
		Size json.RawMessage `json:"size"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return err
	}
	*e = Entry(raw.entryAlias)

	e.Size = ""
	if len(raw.Size) > 0 {
		var s string
		if err := json.Unmarshal(raw.Size, &s); err == nil {
			e.Size = s
		} else {
			var n int64
			if err := json.Unmarshal(raw.Size, &n); err != nil {
				return fmt.Errorf("envdclient: entry size is neither a string nor a number: %s", previewBytes(raw.Size))
			}
			e.Size = strconv.FormatInt(n, 10)
		}
	}
	return nil
}

// SizeBytes returns the size parsed from the string form.
//
// A non-numeric size (empty, or a stat failure) returns 0 rather than an error:
// callers use this for display and byte accounting, and refusing to list a
// directory because one entry had a blank size would be worse than reporting 0.
func (e Entry) SizeBytes() int64 {
	if e.Size == "" {
		return 0
	}
	n, err := strconv.ParseInt(e.Size, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// IsDir reports whether the entry is a directory.
func (e Entry) IsDir() bool { return e.Type == FileTypeDirectory }

// IsSymlink reports whether the entry is a symlink.
func (e Entry) IsSymlink() bool { return e.Type == FileTypeSymlink }

// StatResponse is the body of filesystem.Filesystem/Stat.
type StatResponse struct {
	Entry *Entry `json:"entry"`
}

// Stat returns metadata for one path.
//
// A missing path comes back as *ConnectError with code "not_found"; use
// errors.As plus IsNotFound to distinguish it from a transport failure.
func (c *Client) Stat(ctx context.Context, path string) (Entry, error) {
	if strings.TrimSpace(path) == "" {
		return Entry{}, fmt.Errorf("envdclient: Stat requires a path")
	}
	var resp StatResponse
	if err := c.doUnary(ctx, filesystemService, "Stat", map[string]any{"path": path}, &resp); err != nil {
		return Entry{}, err
	}
	if resp.Entry == nil {
		return Entry{}, fmt.Errorf("envdclient: Stat returned no entry for %q", path)
	}
	return *resp.Entry, nil
}

// ListDirRequest is the body of filesystem.Filesystem/ListDir.
type ListDirRequest struct {
	// Path is the directory to list.
	Path string `json:"path"`

	// Depth greater than 1 recurses. Zero is treated by envd as a single level.
	Depth uint32 `json:"depth,omitempty"`
}

// ListDirResponse is the body of filesystem.Filesystem/ListDir.
type ListDirResponse struct {
	Entries []Entry `json:"entries"`
}

// ListDir lists a directory.
//
// The result is not sorted and is not filtered: envd returns raw directory order,
// including dotfiles. Sorting and any ignore rules belong to the caller, because
// the tool layer has its own conventions that must match the local backend.
func (c *Client) ListDir(ctx context.Context, path string, depth uint32) ([]Entry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("envdclient: ListDir requires a path")
	}
	var resp ListDirResponse
	req := ListDirRequest{Path: path, Depth: depth}
	if err := c.doUnary(ctx, filesystemService, "ListDir", req, &resp); err != nil {
		return nil, err
	}
	return resp.Entries, nil
}

// MakeDir creates a directory.
//
// envd reports an existing directory as code "already_exists" rather than
// succeeding; callers that want idempotence should check IsAlreadyExists.
func (c *Client) MakeDir(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("envdclient: MakeDir requires a path")
	}
	return c.doUnary(ctx, filesystemService, "MakeDir", map[string]any{"path": path}, nil)
}

// Remove deletes a path.
//
// Whether a non-empty directory is removed recursively is envd's decision; this
// client does not assume either way. Treat a "not a directory is empty" style
// error as a signal to recurse explicitly.
func (c *Client) Remove(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("envdclient: Remove requires a path")
	}
	return c.doUnary(ctx, filesystemService, "Remove", map[string]any{"path": path}, nil)
}

// Move renames or moves a path.
//
// The field names are "source" and "destination", confirmed against a live envd.
func (c *Client) Move(ctx context.Context, source, destination string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(destination) == "" {
		return fmt.Errorf("envdclient: Move requires both source and destination")
	}
	return c.doUnary(ctx, filesystemService, "Move", map[string]any{
		"source":      source,
		"destination": destination,
	}, nil)
}

// WatchEvent is one frame from filesystem.Filesystem/WatchDir.
//
// Wire shape confirmed against envd 0.2.11 on 2026-10-28:
//
//	{"start":{}}
//	{"filesystem":{"name":"probe.txt","type":"EVENT_TYPE_CREATE"}}
//
// The first frame is a stream handshake. For change events Name is the entry's
// base name relative to the watched directory and Type is envd's event label
// (EVENT_TYPE_CREATE was observed; other labels may exist). Raw is always
// retained, so a future envd that adds fields does not require a client change.
type WatchEvent struct {
	// Start is true for the initial handshake frame.
	Start bool

	// Name is the changed entry's base name, relative to the watched directory.
	Name string

	// Type is envd's event label, for example EVENT_TYPE_CREATE.
	Type string

	// Raw is the original frame.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a watch frame and retains the raw payload.
func (w *WatchEvent) UnmarshalJSON(payload []byte) error {
	w.Raw = append(w.Raw[:0], payload...)
	w.Start = false
	w.Name = ""
	w.Type = ""

	var wire struct {
		Start      json.RawMessage `json:"start,omitempty"`
		Filesystem *struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"filesystem,omitempty"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if len(wire.Start) > 0 {
		w.Start = true
	}
	if wire.Filesystem != nil {
		w.Name = wire.Filesystem.Name
		w.Type = wire.Filesystem.Type
	}
	w.Raw = append(w.Raw[:0], payload...)
	return nil
}

// WatchDir streams change events for a directory.
//
// The returned reader stays open until ctx is cancelled or the caller closes it,
// so callers must run it in a goroutine or with a cancellable context. Each Recv
// returns one WatchEvent.
func (c *Client) WatchDir(ctx context.Context, path string) (*StreamReader, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("envdclient: WatchDir requires a path")
	}
	return c.Stream(ctx, filesystemService, "WatchDir", map[string]any{"path": path})
}
