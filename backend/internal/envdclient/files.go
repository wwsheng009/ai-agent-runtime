package envdclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FilesPath is envd's native file transfer endpoint.
//
// It is plain HTTP, not Connect-RPC: filesystem.Filesystem exposes no ReadFile or
// WriteFile at all (probed 404; docs/architecture/envd-implementation-report.md
// §2.4), so this endpoint and spawned processes are the only two ways to move
// file bytes through envd.
//
// Measured against a live envd 0.2.11 (2026-10-28), all of it read-only:
//
//	GET  /files?path=<p>&username=<u>          200 + Accept-Ranges: bytes
//	GET  + Range: bytes=0-9                    206 + Content-Range: bytes 0-9/3814
//	GET  + Range whose start is beyond EOF     416 + Content-Range: bytes */3814
//	GET  path is a directory                   400
//	GET  path does not exist                   404 with a JSON body
//	GET  /files/ (trailing slash)              404 "404 page not found" from the mux
//	POST /files (multipart)                    200, O_TRUNC, and it creates parents
//
// Auth: a valid X-Access-Token header is enough. The signature query parameter
// that upstream also supports is only needed for presigned URLs, and we must not
// reimplement it: upstream compares with subtle.ConstantTimeCompare, and a
// hand-rolled copy tends to lose that detail.
//
// Two traps, both found by measurement rather than by reading the source:
//
//   - There is no ETag, and Last-Modified is the *request* time (two calls 2s
//     apart returned 14:20:32 and 14:20:34 for a file whose mtime is years
//     older). Conditional reads therefore never produce 304 for a real client:
//     do not build freshness or revision checks on this endpoint.
//   - Accept-Encoding: gzip is ignored by 0.2.11 even though the current
//     upstream source has that path. Capabilities must be probed, never derived
//     from a version string.
const FilesPath = "/files"

// FilesErrorCode names what a /files reply meant, so a caller branches on
// meaning instead of matching prose.
type FilesErrorCode string

const (
	// FilesNotFound: the path does not exist (or is not a file) on the target.
	FilesNotFound FilesErrorCode = "not_found"
	// FilesIsDirectory: the path is a directory; /files only serves files.
	FilesIsDirectory FilesErrorCode = "is_directory"
	// FilesUnauthorized: envd rejected our credentials for this endpoint. The
	// channel is not usable until the token is right.
	FilesUnauthorized FilesErrorCode = "unauthorized"
	// FilesChannelMissing: the endpoint is not routed at all. Older or proxied
	// deployments can look like this, and the caller must fall back.
	FilesChannelMissing FilesErrorCode = "channel_missing"
	// FilesBadRequest: envd could not act on the request (missing username,
	// unresolvable user, unparsable path).
	FilesBadRequest FilesErrorCode = "bad_request"
	// FilesRangeUnsatisfiable: the requested range cannot be served (start is
	// beyond the file). Only a byte range past EOF produces this; a prefix
	// request whose end runs past EOF is clamped to the file instead.
	FilesRangeUnsatisfiable FilesErrorCode = "range_unsatisfiable"
	// FilesTransport: the request did not complete.
	FilesTransport FilesErrorCode = "transport"
)

// FilesError is one classified /files reply.
type FilesError struct {
	Code    FilesErrorCode
	Status  int    // HTTP status as observed, 0 for transport failures
	Path    string // the path we asked for
	Message string // server text, already shortened
	Err     error  // underlying cause for transport failures
}

func (e *FilesError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("envdclient: /files %s: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("envdclient: /files %s: %s (http %d: %s)", e.Path, e.Code, e.Status, e.Message)
}

func (e *FilesError) Unwrap() error { return e.Err }

// IsFilesCode reports whether err is a *FilesError carrying code.
func IsFilesCode(err error, code FilesErrorCode) bool {
	var filesErr *FilesError
	if errors.As(err, &filesErr) {
		return filesErr.Code == code
	}
	return false
}

// IsFilesMissing reports a path-level miss: not found or a directory. Both mean
// "this file cannot be read here", not "the channel is broken".
func IsFilesMissing(err error) bool {
	return IsFilesCode(err, FilesNotFound) || IsFilesCode(err, FilesIsDirectory)
}

// IsFilesChannelIssue reports whether err says the channel itself is unavailable.
//
// Only these three cases justify falling back to a spawned `cat`. Anything else -
// a missing file, a refused range - is a real answer about the file and must be
// surfaced, because silently retrying through another path is how a permission
// problem gets dressed up as "file is empty".
func IsFilesChannelIssue(err error) bool {
	return IsFilesCode(err, FilesUnauthorized) || IsFilesCode(err, FilesChannelMissing) || IsFilesCode(err, FilesTransport)
}

// FileRead is one successful read through /files.
type FileRead struct {
	// Data holds the bytes, at most the requested cap.
	Data []byte
	// StatusCode is 200 (whole body) or 206 (one range).
	StatusCode int
	// Total is the full file size when the server told us, otherwise -1.
	Total int64
	// Partial is true when the file is larger than Data.
	Partial bool
}

// filesURL builds one /files request URL.
func (c *Client) filesURL(rawPath string) string {
	query := url.Values{}
	query.Set("path", rawPath)
	// Empty username means "whatever the daemon defaults to"; envd answers 400
	// `Query argument username is required` when the parameter is absent, so it
	// is always sent, from the user this client was dialled with.
	query.Set("username", c.user)
	return c.base + FilesPath + "?" + query.Encode()
}

// ReadFile fetches a file through /files.
//
// maxBytes bounds the transfer: when it is positive the request carries
// `Range: bytes=0-<maxBytes-1>`, so a large file costs one windowed read instead
// of a full download, and Partial says there is more. Pass 0 to read the whole
// body; callers that must bound memory should always pass a limit.
func (c *Client) ReadFile(ctx context.Context, path string, maxBytes int64) (*FileRead, error) {
	read, err := c.readFile(ctx, path, maxBytes, true)
	// A prefix request that comes back 416 means this envd build does not clamp
	// an end beyond EOF the way the measured 0.2.11 does. Retry once without the
	// header and bound the body here, so behaviour stays identical across builds
	// instead of turning a working read into an error.
	if err != nil && maxBytes > 0 && IsFilesCode(err, FilesRangeUnsatisfiable) {
		return c.readFile(ctx, path, maxBytes, false)
	}
	return read, err
}

func (c *Client) readFile(ctx context.Context, path string, maxBytes int64, useRange bool) (*FileRead, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.tmo)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.filesURL(path), nil)
	if err != nil {
		return nil, &FilesError{Code: FilesTransport, Path: path, Err: err}
	}
	c.applyHeaders(req)
	// identity, not gzip: 0.2.11 ignores the request but a future build that
	// honours it would hand us a compressed body whose length no longer matches
	// the file, and byte offsets are the whole point of this endpoint.
	req.Header.Set("Accept-Encoding", "identity")
	if useRange && maxBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", maxBytes-1))
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &FilesError{Code: FilesTransport, Path: path, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, classifyFilesReply(resp.StatusCode, contentType, body, path)
	}

	want := maxBytes + 1 // one extra byte answers "is there more?" without a HEAD
	if maxBytes <= 0 {
		want = -1
	}
	var reader io.Reader = resp.Body
	if want > 0 {
		reader = io.LimitReader(resp.Body, want)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, &FilesError{Code: FilesTransport, Path: path, Err: err}
	}

	total := int64(-1)
	if value := parseContentRangeTotal(resp.Header.Get("Content-Range")); value >= 0 {
		total = value
	} else if resp.StatusCode == http.StatusOK && resp.ContentLength >= 0 {
		total = resp.ContentLength
	}

	read := &FileRead{Data: data, StatusCode: resp.StatusCode, Total: total}
	if maxBytes > 0 {
		switch {
		case int64(len(data)) > maxBytes:
			// Should not happen with a Range request; keep the promise anyway.
			read.Data = data[:maxBytes]
			read.Partial = true
		case total > int64(len(read.Data)):
			read.Partial = true
		}
	}
	return read, nil
}

// classifyFilesReply turns a non-2xx /files reply into a FilesError.
//
// The subtle case is 404: envd answers a missing file with a JSON body, while an
// unregistered route answers with the Go mux's plain `404 page not found`. Those
// mean opposite things - the first is an answer about the file, the second says
// the channel is not there - so they must not share a code.
func classifyFilesReply(status int, contentType string, body []byte, path string) *FilesError {
	message := strings.TrimSpace(string(body))
	if len(message) > 200 {
		message = message[:200] + "..."
	}
	fileErr := &FilesError{Status: status, Path: path, Message: message}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		fileErr.Code = FilesUnauthorized
	case status == http.StatusNotFound:
		if isJSONReply(contentType, body) {
			fileErr.Code = FilesNotFound
		} else {
			fileErr.Code = FilesChannelMissing
		}
	case status == http.StatusBadRequest:
		if strings.Contains(message, "is a directory") {
			fileErr.Code = FilesIsDirectory
		} else {
			fileErr.Code = FilesBadRequest
		}
	case status == http.StatusRequestedRangeNotSatisfiable:
		fileErr.Code = FilesRangeUnsatisfiable
	default:
		fileErr.Code = FilesBadRequest
	}
	return fileErr
}

// isJSONReply reports whether the answer looks like envd's JSON error envelope
// rather than the mux's plain-text fallback.
func isJSONReply(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	return strings.HasPrefix(trimmed, "{\"code\":") || strings.HasPrefix(trimmed, "{\"message\":")
}

// parseContentRangeTotal reads the size out of `bytes 0-9/3814` or `bytes */3814`.
// It returns -1 when the header is absent or unreadable, which is normal for 200.
func parseContentRangeTotal(header string) int64 {
	slash := strings.LastIndex(header, "/")
	if slash < 0 || slash == len(header)-1 {
		return -1
	}
	value := strings.TrimSpace(header[slash+1:])
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1
	}
	return parsed
}

// ProbeFiles asks for one path that cannot exist and classifies the answer.
//
// A JSON 404 proves the route is registered and our credentials are accepted,
// which is exactly what a caller needs to know before choosing this channel over
// a spawned `cat`. A 401, or the mux's plain 404, says the channel is not usable
// and the caller should fall back rather than retry per file.
//
// The probe is read-only and creates nothing on the target.
func (c *Client) ProbeFiles(ctx context.Context) error {
	probe := "/.aicli-envd-files-probe-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_, err := c.readFile(ctx, probe, 1, true)
	switch {
	case err == nil:
		// Something answered with bytes for a name that cannot exist. Treat the
		// channel as usable but log nothing: the surprising case is a proxy
		// rewriting paths, and the first real read will show it.
		return nil
	case IsFilesCode(err, FilesNotFound):
		return nil
	default:
		return err
	}
}
