package catalog

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestSQLiteSnapshotStore_SaveAndLoadSnapshot(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
			{Name: "run_tests", Description: "Run tests"},
		},
		Stats: RefreshStats{
			ToolCount: 2,
			Added:     2,
		},
	})
	if err != nil {
		t.Fatalf("save sqlite snapshot: %v", err)
	}

	snapshot, err := store.LoadCatalogSnapshot()
	if err != nil {
		t.Fatalf("load sqlite snapshot: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if len(snapshot.Tools) != 2 || snapshot.Tools[0].Name != "read_logs" {
		t.Fatalf("unexpected snapshot tools: %+v", snapshot)
	}
	if snapshot.Stats.ToolCount != 2 || snapshot.Stats.Added != 2 {
		t.Fatalf("unexpected snapshot stats: %+v", snapshot.Stats)
	}
}

func TestSQLiteSnapshotStore_LoadMissingSnapshotReturnsNil(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	snapshot, err := store.LoadCatalogSnapshot()
	if err != nil {
		t.Fatalf("load empty sqlite snapshot: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected nil snapshot, got %+v", snapshot)
	}
}

func TestCatalog_NewWithSQLiteSnapshotStoreWarmsCatalog(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
		},
		Stats: RefreshStats{
			ToolCount: 1,
		},
	})
	if err != nil {
		t.Fatalf("save sqlite snapshot: %v", err)
	}

	catalog := NewWithStore(store)
	results := catalog.Search("logs", 2)
	if len(results) != 1 || results[0].Name != "read_logs" {
		t.Fatalf("expected sqlite-warmed catalog result, got %+v", results)
	}
}

func TestSQLiteSnapshotStore_SearchCatalogTools(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read application logs"},
			{Name: "run_tests", Description: "Run tests"},
		},
		Stats: RefreshStats{ToolCount: 2},
	})
	if err != nil {
		t.Fatalf("save sqlite snapshot: %v", err)
	}

	results, err := store.SearchCatalogTools("logs", 5)
	if err != nil {
		t.Fatalf("search sqlite catalog tools: %v", err)
	}
	if len(results) == 0 || results[0].Name != "read_logs" {
		t.Fatalf("expected sqlite search to find read_logs, got %+v", results)
	}
}

func TestSQLiteSnapshotStore_SearchCatalogTools_PrefersExactNameAndPhrase(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "fetch_url_content", Description: "Download and read webpage content"},
			{Name: "web_reader", Description: "Fetch url content from websites and summarize it"},
			{Name: "fetcher", Description: "Fetch content"},
		},
		Stats: RefreshStats{ToolCount: 3},
	})
	if err != nil {
		t.Fatalf("save sqlite snapshot: %v", err)
	}

	results, err := store.SearchCatalogTools("fetch url content", 5)
	if err != nil {
		t.Fatalf("search sqlite catalog tools: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected sqlite search results")
	}
	if results[0].Name != "fetch_url_content" {
		t.Fatalf("expected exact-name phrase hit first, got %+v", results)
	}
}

func TestSQLiteSnapshotStore_SaveCatalogSnapshot_IncrementalSync(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
			{Name: "run_tests", Description: "Run tests"},
		},
		Stats: RefreshStats{ToolCount: 2},
	})
	if err != nil {
		t.Fatalf("save initial sqlite snapshot: %v", err)
	}

	err = store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read application logs"},
			{Name: "search_docs", Description: "Search docs"},
		},
		Stats: RefreshStats{ToolCount: 2, Added: 1, Removed: 1, Updated: 1},
	})
	if err != nil {
		t.Fatalf("save incremental sqlite snapshot: %v", err)
	}

	results, err := store.SearchCatalogTools("application logs", 5)
	if err != nil {
		t.Fatalf("search updated sqlite catalog tools: %v", err)
	}
	if len(results) == 0 || results[0].Name != "read_logs" {
		t.Fatalf("expected updated read_logs search result, got %+v", results)
	}

	results, err = store.SearchCatalogTools("tests", 5)
	if err != nil {
		t.Fatalf("search removed sqlite catalog tools: %v", err)
	}
	for _, tool := range results {
		if tool.Name == "run_tests" {
			t.Fatalf("did not expect removed tool to remain searchable: %+v", results)
		}
	}

	assertSQLiteCount(t, store.db, "SELECT COUNT(*) FROM catalog_tools", 2)
	if store.ftsEnabled {
		assertSQLiteCount(t, store.db, "SELECT COUNT(*) FROM catalog_tools_fts5", 2)
	}
}

func assertSQLiteCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if got != want {
		t.Fatalf("expected count %d, got %d for query %q", want, got, query)
	}
}
