package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
)

func TestSPAHandlerServesAssetsAndClientRoutes(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html>app</html>")},
		"assets/app-123.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
	handler := newSPAHandler(assets)

	tests := []struct {
		name        string
		requestPath string
		wantStatus  int
		wantBody    string
		wantCache   string
	}{
		{
			name:        "root serves index",
			requestPath: "/",
			wantStatus:  http.StatusOK,
			wantBody:    "<html>app</html>",
			wantCache:   "no-cache",
		},
		{
			name:        "client route falls back to index",
			requestPath: "/workspace/session-1",
			wantStatus:  http.StatusOK,
			wantBody:    "<html>app</html>",
			wantCache:   "no-cache",
		},
		{
			name:        "asset is served directly",
			requestPath: "/assets/app-123.js",
			wantStatus:  http.StatusOK,
			wantBody:    "console.log('app')",
			wantCache:   "public, max-age=31536000, immutable",
		},
		{
			name:        "unknown api route remains not found",
			requestPath: "/api/missing",
			wantStatus:  http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assert.Equal(t, tt.wantStatus, response.Code)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, response.Body.String())
			}
			assert.Equal(t, tt.wantCache, response.Header().Get("Cache-Control"))
		})
	}
}

func TestSPAHandlerRejectsUnsupportedMethod(t *testing.T) {
	handler := newSPAHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("app")},
	})
	request := httptest.NewRequest(http.MethodPost, "/workspace", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestAvailableRequiresIndexFile(t *testing.T) {
	assert.False(t, available(fstest.MapFS{
		"placeholder.txt": &fstest.MapFile{Data: []byte("placeholder")},
	}))
	assert.True(t, available(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("app")},
	}))
}

func TestAssetManifestHashIsStableAndExcludesBuildInfo(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte(`<script type="module" src="/assets/main.js"></script>`)},
		"assets/main.js":  &fstest.MapFile{Data: []byte("console.log('main')")},
		"build-info.json": &fstest.MapFile{Data: []byte(`{"build_time":"one"}`)},
	}
	first, err := AssetManifestHash(assets)
	if err != nil {
		t.Fatalf("AssetManifestHash: %v", err)
	}

	assets["build-info.json"] = &fstest.MapFile{Data: []byte(`{"build_time":"two"}`)}
	second, err := AssetManifestHash(assets)
	if err != nil {
		t.Fatalf("AssetManifestHash after build-info change: %v", err)
	}
	if first != second {
		t.Fatalf("build-info.json changed manifest hash: %q != %q", first, second)
	}

	assets["assets/main.js"] = &fstest.MapFile{Data: []byte("console.log('changed')")}
	third, err := AssetManifestHash(assets)
	if err != nil {
		t.Fatalf("AssetManifestHash after asset change: %v", err)
	}
	if third == first {
		t.Fatalf("asset content change did not change manifest hash %q", third)
	}
}

func TestFrontendProvenanceReadsBuildInfoAndEntryAsset(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte(`<!doctype html><html><head><script crossorigin type="module" src="/assets/main.js"></script></head></html>`)},
		"assets/main.js": &fstest.MapFile{Data: []byte("console.log('main')")},
	}
	manifestHash, err := AssetManifestHash(assets)
	if err != nil {
		t.Fatalf("AssetManifestHash: %v", err)
	}
	buildInfo, err := json.Marshal(FrontendProvenance{
		AssetManifestHash: "stale-value",
		BuildTime:         "2026-07-12T08:30:00Z",
		EntryAsset:        "stale-entry.js",
	})
	if err != nil {
		t.Fatalf("Marshal build info: %v", err)
	}
	assets[frontendBuildInfoFile] = &fstest.MapFile{Data: buildInfo}

	actual, err := provenanceFromFS(assets)
	if err != nil {
		t.Fatalf("provenanceFromFS: %v", err)
	}
	if actual.AssetManifestHash != manifestHash {
		t.Fatalf("expected computed manifest hash %q, got %q", manifestHash, actual.AssetManifestHash)
	}
	if actual.BuildTime != "2026-07-12T08:30:00Z" {
		t.Fatalf("unexpected build time %q", actual.BuildTime)
	}
	if actual.EntryAsset != "/assets/main.js" {
		t.Fatalf("unexpected entry asset %q", actual.EntryAsset)
	}
}
