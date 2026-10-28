package envdclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type filesystemCall struct {
	Path        string
	ContentType string
	Body        map[string]any
}

// newFilesystemStub serves canned responses for the filesystem RPCs and records
// each request, so the whole method surface is covered without a live envd.
func newFilesystemStub(t *testing.T) (*httptest.Server, *[]filesystemCall) {
	t.Helper()
	var calls []filesystemCall

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &body)
		}
		calls = append(calls, filesystemCall{
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Body:        body,
		})
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/filesystem.Filesystem/Stat":
			_, _ = io.WriteString(w, `{"entry":{"name":"app","type":"FILE_TYPE_DIRECTORY","path":"/srv/app","size":"4096","mode":493,"permissions":"drwxr-xr-x","owner":"n9e","group":"n9e","modifiedTime":"2026-10-28T08:00:00Z"}}`)
		case "/filesystem.Filesystem/ListDir":
			_, _ = io.WriteString(w, `{"entries":[{"name":"a.txt","type":"FILE_TYPE_FILE","path":"/srv/app/a.txt","size":"12"},{"name":"sub","type":"FILE_TYPE_DIRECTORY","path":"/srv/app/sub","size":"4096"}]}`)
		case "/filesystem.Filesystem/MakeDir", "/filesystem.Filesystem/Remove", "/filesystem.Filesystem/Move":
			_, _ = io.WriteString(w, `{}`)
		case "/filesystem.Filesystem/WatchDir":
			frame, err := EncodeJSONFrame(map[string]any{
				"name": "a.txt",
				"path": "/srv/app/a.txt",
				"op":   "write",
			})
			if err != nil {
				t.Errorf("EncodeJSONFrame: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/connect+json")
			_, _ = w.Write(frame)
			_, _ = w.Write(EncodeFrame(flagEndStream, []byte("{}")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestFilesystemUnaryCalls covers Stat/ListDir/MakeDir/Remove/Move and the wire
// shape of their requests.
func TestFilesystemUnaryCalls(t *testing.T) {
	srv, calls := newFilesystemStub(t)
	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	entry, err := client.Stat(ctx, "/srv/app")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !entry.IsDir() || entry.SizeBytes() != 4096 || entry.Permissions != "drwxr-xr-x" {
		t.Fatalf("Stat entry = %+v, want a 4096-byte directory", entry)
	}

	entries, err := client.ListDir(ctx, "/srv/app", 2)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 2 || entries[0].SizeBytes() != 12 || !entries[1].IsDir() {
		t.Fatalf("ListDir entries = %+v, want the stub's two entries", entries)
	}

	if err := client.MakeDir(ctx, "/srv/app/new"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := client.Remove(ctx, "/srv/app/old"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := client.Move(ctx, "/srv/app/a", "/srv/app/b"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if len(*calls) != 5 {
		t.Fatalf("recorded %d calls, want 5", len(*calls))
	}
	for _, call := range *calls {
		if call.ContentType != "application/json" {
			t.Errorf("%s content-type = %q, want application/json", call.Path, call.ContentType)
		}
	}
	if got := (*calls)[1].Body["depth"]; got != float64(2) {
		t.Errorf("ListDir depth = %v, want 2", got)
	}
	if got := (*calls)[2].Body["path"]; got != "/srv/app/new" {
		t.Errorf("MakeDir path = %v, want /srv/app/new", got)
	}
	if got := (*calls)[3].Body["path"]; got != "/srv/app/old" {
		t.Errorf("Remove path = %v, want /srv/app/old", got)
	}
	move := (*calls)[4].Body
	if move["source"] != "/srv/app/a" || move["destination"] != "/srv/app/b" {
		t.Errorf("Move body = %v, want source/destination", move)
	}
}

// TestWatchDirDecodesEventFrames covers the streaming filesystem method.
func TestWatchDirDecodesEventFrames(t *testing.T) {
	srv, _ := newFilesystemStub(t)
	client, err := Dial(Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	stream, err := client.WatchDir(context.Background(), "/srv/app")
	if err != nil {
		t.Fatalf("WatchDir: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var event WatchEvent
	if err := stream.Recv(&event); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if event.Name != "a.txt" || event.Path != "/srv/app/a.txt" || event.Op != "write" {
		t.Fatalf("event = %+v, want the stub event", event)
	}
	if len(event.Raw) == 0 {
		t.Fatal("Raw payload is empty, want the original frame")
	}
	if err := stream.Recv(&event); err != io.EOF {
		t.Fatalf("second Recv = %v, want io.EOF after the end-stream frame", err)
	}
}
