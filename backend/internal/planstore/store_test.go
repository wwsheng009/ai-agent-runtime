package planstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIDForSlugsPaths(t *testing.T) {
	cases := []struct {
		name    string
		project string
		plan    string
		want    string
	}{
		{"windows backslashes", `E:\work\proj`, `docs/plan.md`, "proj/plan"},
		{"windows plan backslash", `E:\work\proj`, `docs\plan.md`, "proj/plan"},
		{"trailing separators", `E:\work\proj\`, `docs/plan.md`, "proj/plan"},
		{"unix paths", "/home/user/my-proj", "docs/Plan-v2.MD", "my-proj/plan-v2"},
		{"keeps dash and underscore", "My_Proj-2", "release_notes.md", "my_proj-2/release_notes"},
		{"cjk and spaces collapse", `C:\work\项目 Alpha`, `docs\设计 plan v2.md`, "alpha/plan-v2"},
		{"cjk only falls back", `C:\work\我的项目`, `docs\计划.md`, "project/plan"},
		{"empty falls back", "", "", "project/plan"},
		{"plan without extension", "proj", "PLAN", "proj/plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IDFor(tc.project, tc.plan)
			if got != tc.want {
				t.Fatalf("IDFor(%q, %q) = %q, want %q", tc.project, tc.plan, got, tc.want)
			}
			for _, part := range strings.Split(got, "/") {
				if part == "" || part != slug(part) {
					t.Fatalf("IDFor(%q, %q) = %q has a non-slugged component %q", tc.project, tc.plan, got, part)
				}
			}
		})
	}
}

func TestSlugFoldsForeignCharacters(t *testing.T) {
	cases := map[string]string{
		"设计 plan v2": "plan-v2",
		"我的项目":       "",
		"a  b":       "a-b",
		"UPPER_case": "upper_case",
		"!!!":        "",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Fatalf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultRootAndNewStoreFallback(t *testing.T) {
	store := NewStore("")
	if store.Root() != DefaultRoot() {
		t.Fatalf("NewStore(\"\").Root() = %q, want DefaultRoot() = %q", store.Root(), DefaultRoot())
	}
	if !strings.Contains(filepath.ToSlash(DefaultRoot()), ".aicli/plans") {
		t.Fatalf("DefaultRoot() = %q, want it to contain .aicli/plans", DefaultRoot())
	}
}

func TestDefaultRootHonoursHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, want := DefaultRoot(), filepath.Join(home, ".aicli", "plans"); got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestRecordUpsertPreservesState(t *testing.T) {
	store := NewStore(t.TempDir())

	rec, err := store.Record(RecordOptions{
		ProjectPath: `E:\work\proj`,
		PlanPath:    "docs/plan.md",
		SessionID:   "session-1",
		Title:       "Draft",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if rec.ID != "proj/plan" {
		t.Fatalf("Record ID = %q, want proj/plan", rec.ID)
	}
	if rec.ProjectSlug != "proj" {
		t.Fatalf("ProjectSlug = %q, want proj", rec.ProjectSlug)
	}
	if rec.Status != StatusPending {
		t.Fatalf("new record status = %q, want %q", rec.Status, StatusPending)
	}
	if rec.Version != 0 || len(rec.Rounds) != 0 {
		t.Fatalf("new record Version/Rounds = %d/%d, want 0/0", rec.Version, len(rec.Rounds))
	}
	if rec.CreatedAt == "" || rec.UpdatedAt == "" {
		t.Fatalf("new record timestamps must be set: %+v", rec)
	}

	snap, err := store.Snapshot(SnapshotOptions{
		ID:       rec.ID,
		Decision: "enter",
		Source:   "model",
		Content:  []byte("# plan v1\n"),
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := store.SetStatus(rec.ID, StatusApproved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	updated, err := store.Record(RecordOptions{
		ID:        rec.ID,
		SessionID: "session-2",
		Title:     "Final",
	})
	if err != nil {
		t.Fatalf("Record upsert: %v", err)
	}
	if updated.Status != StatusApproved {
		t.Fatalf("upsert changed status to %q, want %q", updated.Status, StatusApproved)
	}
	if updated.Version != 1 || len(updated.Rounds) != 1 {
		t.Fatalf("upsert changed Version/Rounds to %d/%d, want 1/1", updated.Version, len(updated.Rounds))
	}
	if updated.Title != "Final" || updated.SessionID != "session-2" {
		t.Fatalf("upsert did not refresh metadata: %+v", updated)
	}
	if updated.ProjectPath != `E:\work\proj` || updated.PlanPath != "docs/plan.md" {
		t.Fatalf("upsert dropped previous paths: %+v", updated)
	}
	if updated.CreatedAt != rec.CreatedAt {
		t.Fatalf("upsert changed CreatedAt from %q to %q", rec.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt < snap.UpdatedAt {
		t.Fatalf("UpdatedAt went backwards: %q < %q", updated.UpdatedAt, snap.UpdatedAt)
	}

	stored, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get after upsert = (%+v, %v, %v)", stored, ok, err)
	}
	if stored.Title != "Final" || stored.Status != StatusApproved || stored.Version != 1 {
		t.Fatalf("stored record mismatch: %+v", stored)
	}
}

func TestSnapshotWritesVersionedFiles(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	rec, err := store.Record(RecordOptions{
		ProjectPath: filepath.Join("work", "proj"),
		PlanPath:    "docs/plan.md",
		Title:       "Plan",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	first, err := store.Snapshot(SnapshotOptions{
		ID:       rec.ID,
		Decision: "enter",
		Notes:    "first draft",
		Source:   "model",
		Content:  []byte("# v1\n"),
		Status:   StatusPending,
	})
	if err != nil {
		t.Fatalf("Snapshot v1: %v", err)
	}
	if first.Version != 1 || len(first.Rounds) != 1 {
		t.Fatalf("first snapshot Version/Rounds = %d/%d, want 1/1", first.Version, len(first.Rounds))
	}
	if first.Status != StatusPending {
		t.Fatalf("first snapshot status = %q, want %q", first.Status, StatusPending)
	}
	if got, want := first.Rounds[0].Snapshot, "versions/proj/plan-v1.md"; got != want {
		t.Fatalf("round snapshot = %q, want %q", got, want)
	}
	if first.Rounds[0].Decision != "enter" || first.Rounds[0].Source != "model" || first.Rounds[0].Notes != "first draft" {
		t.Fatalf("round metadata mismatch: %+v", first.Rounds[0])
	}
	v1Path := filepath.Join(root, "versions", "proj", "plan-v1.md")
	if data, err := os.ReadFile(v1Path); err != nil || string(data) != "# v1\n" {
		t.Fatalf("ReadFile(%s) = (%q, %v), want plan body v1", v1Path, string(data), err)
	}

	second, err := store.Snapshot(SnapshotOptions{
		ID:       rec.ID,
		Decision: "approve",
		Source:   "user",
		Content:  []byte("# v2\n"),
		Status:   StatusApproved,
	})
	if err != nil {
		t.Fatalf("Snapshot v2: %v", err)
	}
	if second.Version != 2 || len(second.Rounds) != 2 {
		t.Fatalf("second snapshot Version/Rounds = %d/%d, want 2/2", second.Version, len(second.Rounds))
	}
	if second.Status != StatusApproved {
		t.Fatalf("second snapshot status = %q, want %q", second.Status, StatusApproved)
	}
	v2Path := filepath.Join(root, "versions", "proj", "plan-v2.md")
	if data, err := os.ReadFile(v2Path); err != nil || string(data) != "# v2\n" {
		t.Fatalf("ReadFile(%s) = (%q, %v), want plan body v2", v2Path, string(data), err)
	}
	if data, err := os.ReadFile(v1Path); err != nil || string(data) != "# v1\n" {
		t.Fatalf("v1 snapshot was rewritten: (%q, %v)", string(data), err)
	}

	if data, err := store.ReadVersion(rec.ID, 1); err != nil || string(data) != "# v1\n" {
		t.Fatalf("ReadVersion(1) = (%q, %v)", string(data), err)
	}
	if data, err := store.ReadVersion(rec.ID, 2); err != nil || string(data) != "# v2\n" {
		t.Fatalf("ReadVersion(2) = (%q, %v)", string(data), err)
	}
	if data, err := store.ReadLatest(rec.ID); err != nil || string(data) != "# v2\n" {
		t.Fatalf("ReadLatest = (%q, %v), want v2", string(data), err)
	}

	third, err := store.Snapshot(SnapshotOptions{
		ID:       rec.ID,
		Decision: "request_changes",
		Content:  []byte("# v3\n"),
	})
	if err != nil {
		t.Fatalf("Snapshot v3: %v", err)
	}
	if third.Status != StatusApproved {
		t.Fatalf("empty SnapshotOptions.Status changed status to %q, want %q", third.Status, StatusApproved)
	}
	if third.Version != 3 || len(third.Rounds) != 3 {
		t.Fatalf("third snapshot Version/Rounds = %d/%d, want 3/3", third.Version, len(third.Rounds))
	}
	if third.Rounds[2].Version != 3 || third.Rounds[2].CreatedAt == "" {
		t.Fatalf("third round mismatch: %+v", third.Rounds[2])
	}
}

func TestListOrdersByUpdatedAtDescAndReturnsCopies(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := store.Record(RecordOptions{ID: "proj/" + name, Title: name}); err != nil {
			t.Fatalf("Record %s: %v", name, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List returned %d records, want 3", len(list))
	}
	wantOrder := []string{"proj/gamma", "proj/beta", "proj/alpha"}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Fatalf("List[%d].ID = %q, want %q (order %v)", i, list[i].ID, want, list)
		}
	}
	if list[0].UpdatedAt <= list[2].UpdatedAt {
		t.Fatalf("UpdatedAt not descending: %q <= %q", list[0].UpdatedAt, list[2].UpdatedAt)
	}

	// Mutating a returned record must not leak into the store.
	list[0].Title = "hacked"
	list[0].Status = StatusApproved
	list[0].Rounds = append(list[0].Rounds, Round{Version: 99})
	again, err := store.List()
	if err != nil {
		t.Fatalf("List again: %v", err)
	}
	if again[0].Title != "gamma" || again[0].Status != StatusPending || len(again[0].Rounds) != 0 {
		t.Fatalf("List returned shared state: %+v", again[0])
	}

	got, ok, err := store.Get("proj/alpha")
	if err != nil || !ok {
		t.Fatalf("Get = (%+v, %v, %v)", got, ok, err)
	}
	got.Title = "hacked"
	got.Rounds = append(got.Rounds, Round{Version: 42})
	fresh, ok, err := store.Get("proj/alpha")
	if err != nil || !ok {
		t.Fatalf("Get again = (%+v, %v, %v)", fresh, ok, err)
	}
	if fresh.Title != "alpha" || len(fresh.Rounds) != 0 {
		t.Fatalf("Get returned shared state: %+v", fresh)
	}
}

func TestSetStatusUpdatesRecord(t *testing.T) {
	store := NewStore(t.TempDir())
	rec, err := store.Record(RecordOptions{ID: "proj/plan", Title: "Plan"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	before := rec.UpdatedAt

	got, err := store.SetStatus(rec.ID, StatusNotImplemented)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.Status != StatusNotImplemented {
		t.Fatalf("SetStatus returned status %q, want %q", got.Status, StatusNotImplemented)
	}
	if got.UpdatedAt < before {
		t.Fatalf("SetStatus moved UpdatedAt backwards: %q < %q", got.UpdatedAt, before)
	}
	if got.Version != 0 || len(got.Rounds) != 0 {
		t.Fatalf("SetStatus touched Version/Rounds: %+v", got)
	}

	loaded, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get = (%+v, %v, %v)", loaded, ok, err)
	}
	if loaded.Status != StatusNotImplemented {
		t.Fatalf("persisted status = %q, want %q", loaded.Status, StatusNotImplemented)
	}
}

func TestMissingRecordsReportErrNotFound(t *testing.T) {
	store := NewStore(t.TempDir())
	rec, err := store.Record(RecordOptions{ID: "proj/plan"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok, err := store.Get("ghost/plan")
	if err != nil {
		t.Fatalf("Get(missing) error = %v, want nil", err)
	}
	if ok || got.ID != "" || got.Status != "" || got.Version != 0 || len(got.Rounds) != 0 {
		t.Fatalf("Get(missing) = (%+v, %v), want zero record and false", got, ok)
	}

	if _, err := store.SetStatus("ghost/plan", StatusApproved); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := store.Snapshot(SnapshotOptions{ID: "ghost/plan", Content: []byte("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Snapshot(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := store.Snapshot(SnapshotOptions{Content: []byte("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Snapshot(empty id) error = %v, want ErrNotFound", err)
	}
	if _, err := store.ReadVersion("ghost/plan", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadVersion(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := store.ReadLatest("ghost/plan"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadLatest(missing) error = %v, want ErrNotFound", err)
	}

	// Existing record, unknown version / no snapshot yet.
	if _, err := store.ReadVersion(rec.ID, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadVersion(unknown version) error = %v, want ErrNotFound", err)
	}
	if _, err := store.ReadLatest(rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadLatest(no snapshot) error = %v, want ErrNotFound", err)
	}
}

func TestMissingIndexIsAnEmptyStore(t *testing.T) {
	store := NewStore(t.TempDir())
	records, err := store.List()
	if err != nil {
		t.Fatalf("List on missing index: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("List on missing index returned %d records, want 0", len(records))
	}
	if _, ok, err := store.Get("proj/plan"); err != nil || ok {
		t.Fatalf("Get on missing index = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestCorruptIndexErrorsAndIsPreserved(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if _, err := store.Record(RecordOptions{ID: "proj/plan", Title: "Plan"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	indexPath := filepath.Join(root, "index.json")
	corrupt := []byte("{ this is not json")
	if err := os.WriteFile(indexPath, corrupt, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := store.List(); err == nil {
		t.Fatal("List on corrupt index returned nil error")
	}
	if _, _, err := store.Get("proj/plan"); err == nil {
		t.Fatal("Get on corrupt index returned nil error")
	}
	if _, err := store.Record(RecordOptions{ID: "proj/other"}); err == nil {
		t.Fatal("Record on corrupt index returned nil error")
	}
	if _, err := store.SetStatus("proj/plan", StatusApproved); err == nil {
		t.Fatal("SetStatus on corrupt index returned nil error")
	}
	if _, err := store.Snapshot(SnapshotOptions{ID: "proj/plan", Content: []byte("x")}); err == nil {
		t.Fatal("Snapshot on corrupt index returned nil error")
	}
	if _, err := store.ReadVersion("proj/plan", 1); err == nil {
		t.Fatal("ReadVersion on corrupt index returned nil error")
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(corrupt) {
		t.Fatalf("corrupt index was rewritten: %q", string(after))
	}
}

func TestIndexFileLayout(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if _, err := store.Record(RecordOptions{ID: "proj/plan", Title: "Plan"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatalf("ReadFile(index.json): %v", err)
	}
	var idx struct {
		Version int      `json:"version"`
		Records []Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json is not valid JSON: %v", err)
	}
	if idx.Version != 1 {
		t.Fatalf("index version = %d, want 1", idx.Version)
	}
	if len(idx.Records) != 1 || idx.Records[0].ID != "proj/plan" || idx.Records[0].Status != StatusPending {
		t.Fatalf("index records mismatch: %+v", idx.Records)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestSnapshotSlugsCustomIDForFileNames(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	rec, err := store.Record(RecordOptions{ID: "My Proj/Design Doc", Title: "Doc"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if rec.ProjectSlug != "my-proj" {
		t.Fatalf("ProjectSlug = %q, want my-proj", rec.ProjectSlug)
	}

	snap, err := store.Snapshot(SnapshotOptions{ID: rec.ID, Content: []byte("# doc\n")})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got, want := snap.Rounds[0].Snapshot, "versions/my-proj/design-doc-v1.md"; got != want {
		t.Fatalf("snapshot path = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(root, "versions", "my-proj", "design-doc-v1.md")); err != nil || string(data) != "# doc\n" {
		t.Fatalf("snapshot file = (%q, %v)", string(data), err)
	}
}
