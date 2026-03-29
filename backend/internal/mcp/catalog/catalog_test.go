package catalog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
)

type fakeSnapshotStore struct {
	snapshot *Snapshot
	saved    *Snapshot
}

func (s *fakeSnapshotStore) LoadCatalogSnapshot() (*Snapshot, error) {
	return s.snapshot, nil
}

func (s *fakeSnapshotStore) SaveCatalogSnapshot(snapshot Snapshot) error {
	cloned := Snapshot{
		Tools: cloneTools(snapshot.Tools),
		Stats: snapshot.Stats,
	}
	s.saved = &cloned
	return nil
}

func TestCatalog_SearchReturnsRelevantTools(t *testing.T) {
	catalog := New()
	catalog.Refresh([]skill.ToolInfo{
		{
			Name:        "read_logs",
			Description: "Read and search application logs",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a file from workspace",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "run_tests",
			Description: "Run tests and inspect failures",
		},
	})

	results := catalog.Search("inspect error logs", 2)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Name != "read_logs" {
		t.Fatalf("expected read_logs to rank first, got %s", results[0].Name)
	}
}

type gatewayToolSource struct {
	tools []skill.ToolInfo
}

func (s *gatewayToolSource) ListTools() []skill.ToolInfo {
	return s.tools
}

func TestGateway_RefreshAndSearch(t *testing.T) {
	source := &gatewayToolSource{
		tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
			{Name: "run_tests", Description: "Run tests"},
		},
	}
	gateway := NewGateway(source, nil)

	results := gateway.Search("logs", 2)
	if len(results) == 0 {
		t.Fatal("expected search results after refresh")
	}
	if results[0].Name != "read_logs" {
		t.Fatalf("expected read_logs, got %s", results[0].Name)
	}
}

func TestCatalog_SearchPrefersExactPhraseMatch(t *testing.T) {
	catalog := New()
	catalog.Refresh([]skill.ToolInfo{
		{
			Name:        "logs_reader",
			Description: "Read logs from services",
		},
		{
			Name:        "read_logs",
			Description: "Read logs with structured query",
		},
		{
			Name:        "read_file",
			Description: "Read file content",
		},
	})

	results := catalog.Search("read logs", 2)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Name != "read_logs" {
		t.Fatalf("expected read_logs to rank first for exact phrase, got %s", results[0].Name)
	}
}

func TestCatalog_SearchUsesCoverageWhenScoresClose(t *testing.T) {
	catalog := New()
	catalog.Refresh([]skill.ToolInfo{
		{
			Name:        "search_docs",
			Description: "Search docs and gather requirements",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "summarize_docs",
			Description: "Summarize docs",
		},
		{
			Name:        "run_tests",
			Description: "Run tests",
		},
	})

	results := catalog.Search("search docs query", 3)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Name != "search_docs" {
		t.Fatalf("expected search_docs to rank first with highest token coverage, got %s", results[0].Name)
	}
}

func TestCatalog_SearchPrefersExactNameOverDescriptionOnlyMatch(t *testing.T) {
	catalog := New()
	catalog.Refresh([]skill.ToolInfo{
		{
			Name:        "fetch_url_content",
			Description: "Download and read webpage content",
		},
		{
			Name:        "web_reader",
			Description: "Fetch url content from websites and summarize it",
		},
	})

	results := catalog.Search("fetch url content", 2)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Name != "fetch_url_content" {
		t.Fatalf("expected exact name hit to rank first, got %s", results[0].Name)
	}
}

func TestCatalog_RefreshStatsTracksAddedRemovedAndUpdated(t *testing.T) {
	catalog := New()
	stats := catalog.Refresh([]skill.ToolInfo{
		{Name: "read_logs", Description: "Read logs"},
		{Name: "run_tests", Description: "Run tests"},
	})
	if stats.ToolCount != 2 || stats.Added != 2 || stats.Removed != 0 || stats.Updated != 0 {
		t.Fatalf("unexpected initial refresh stats: %+v", stats)
	}

	stats = catalog.Refresh([]skill.ToolInfo{
		{Name: "read_logs", Description: "Read application logs"},
		{Name: "search_docs", Description: "Search docs"},
	})
	if stats.ToolCount != 2 {
		t.Fatalf("expected tool count 2, got %+v", stats)
	}
	if stats.Added != 1 || stats.Removed != 1 || stats.Updated != 1 {
		t.Fatalf("unexpected delta refresh stats: %+v", stats)
	}
	if stats.LastRefreshAt.IsZero() {
		t.Fatalf("expected refresh timestamp, got %+v", stats)
	}
}

func TestCatalog_NewWithStoreLoadsSnapshot(t *testing.T) {
	store := &fakeSnapshotStore{
		snapshot: &Snapshot{
			Tools: []skill.ToolInfo{
				{Name: "read_logs", Description: "Read logs"},
			},
			Stats: RefreshStats{
				ToolCount:     1,
				Added:         1,
				LastRefreshAt: time.Now().UTC(),
			},
		},
	}

	catalog := NewWithStore(store)
	results := catalog.Search("logs", 5)
	if len(results) != 1 {
		t.Fatalf("expected snapshot-backed tool, got %d", len(results))
	}
	if results[0].Name != "read_logs" {
		t.Fatalf("expected read_logs from snapshot, got %s", results[0].Name)
	}
	if stats := catalog.RefreshStats(); stats.ToolCount != 1 {
		t.Fatalf("expected refresh stats from snapshot, got %+v", stats)
	}
}

func TestCatalog_RefreshPersistsSnapshotToStore(t *testing.T) {
	store := &fakeSnapshotStore{}
	catalog := NewWithStore(store)

	stats := catalog.Refresh([]skill.ToolInfo{
		{Name: "read_logs", Description: "Read logs"},
		{Name: "run_tests", Description: "Run tests"},
	})
	if stats.ToolCount != 2 {
		t.Fatalf("expected tool count 2, got %+v", stats)
	}
	if store.saved == nil {
		t.Fatal("expected snapshot to be saved")
	}
	if len(store.saved.Tools) != 2 {
		t.Fatalf("expected 2 saved tools, got %+v", store.saved)
	}
	if store.saved.Stats.ToolCount != 2 || store.saved.Stats.LastRefreshAt.IsZero() {
		t.Fatalf("expected saved stats to be populated, got %+v", store.saved.Stats)
	}
}

func TestNewGatewayWithStore_UsesSnapshotBackedCatalog(t *testing.T) {
	store := &fakeSnapshotStore{
		snapshot: &Snapshot{
			Tools: []skill.ToolInfo{
				{Name: "read_logs", Description: "Read logs"},
			},
			Stats: RefreshStats{ToolCount: 1},
		},
	}
	source := &gatewayToolSource{
		tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
		},
	}

	gateway := NewGatewayWithStore(source, store)
	results := gateway.Search("logs", 2)
	if len(results) != 1 || results[0].Name != "read_logs" {
		t.Fatalf("expected snapshot-backed gateway result, got %+v", results)
	}
}

func TestFileSnapshotStore_SaveAndLoadSnapshot(t *testing.T) {
	store := NewFileSnapshotStore(filepath.Join(t.TempDir(), "catalog", "snapshot.json"))

	err := store.SaveCatalogSnapshot(Snapshot{
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
		t.Fatalf("save snapshot: %v", err)
	}

	snapshot, err := store.LoadCatalogSnapshot()
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected snapshot")
	}
	if len(snapshot.Tools) != 2 || snapshot.Tools[0].Name != "read_logs" {
		t.Fatalf("unexpected loaded snapshot: %+v", snapshot)
	}
	if snapshot.Stats.ToolCount != 2 || snapshot.Stats.Added != 2 {
		t.Fatalf("unexpected loaded stats: %+v", snapshot.Stats)
	}
}

func TestFileSnapshotStore_LoadMissingSnapshotReturnsNil(t *testing.T) {
	store := NewFileSnapshotStore(filepath.Join(t.TempDir(), "missing.json"))
	snapshot, err := store.LoadCatalogSnapshot()
	if err != nil {
		t.Fatalf("load missing snapshot: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected nil snapshot for missing file, got %+v", snapshot)
	}
}

func TestCatalog_NewWithFileSnapshotStoreWarmsCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog_snapshot.json")
	store := NewFileSnapshotStore(path)
	err := store.SaveCatalogSnapshot(Snapshot{
		Tools: []skill.ToolInfo{
			{Name: "read_logs", Description: "Read logs"},
		},
		Stats: RefreshStats{
			ToolCount: 1,
		},
	})
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	catalog := NewWithStore(store)
	results := catalog.Search("logs", 2)
	if len(results) != 1 || results[0].Name != "read_logs" {
		t.Fatalf("expected warmed catalog result, got %+v", results)
	}
}

func TestCatalog_SearchUsesStoreBackedSearchWhenAvailable(t *testing.T) {
	store, err := NewSQLiteSnapshotStore(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatalf("create sqlite snapshot store: %v", err)
	}
	defer func() { _ = store.Close() }()

	catalog := NewWithStore(store)
	catalog.Refresh([]skill.ToolInfo{
		{Name: "read_logs", Description: "Read application logs"},
		{Name: "run_tests", Description: "Run tests"},
	})

	results := catalog.Search("logs", 5)
	if len(results) == 0 || results[0].Name != "read_logs" {
		t.Fatalf("expected store-backed search to find read_logs, got %+v", results)
	}
}
