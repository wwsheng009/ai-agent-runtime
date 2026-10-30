package remotetools_test

import (
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestEveryPathToolDeclaresWorkdir guards the per-call workdir contract from the
// outside, over the whole registered surface.
//
// The wrapper injects workdir by looking for a known path parameter name. A new
// tool that spells its path argument differently would silently lose directory
// support with no test failing, so the expectation is stated here instead of in
// a hand-maintained list: every tool that takes a path must declare workdir, and
// no tool without a path may advertise one. shell and job_start are exempt from
// the second half because they resolve workdir themselves rather than through
// the injector.
func TestEveryPathToolDeclaresWorkdir(t *testing.T) {
	server, err := remotetools.NewServer(newTestConfig(t, t.TempDir()), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	pathParams := map[string]bool{
		"path":        true,
		"paths":       true,
		"files":       true,
		"file_path":   true,
		"source":      true,
		"destination": true,
		"patch":       true,
	}
	// These resolve workdir in the handler, so they legitimately declare it
	// without carrying a path argument of their own.
	nativeWorkdir := map[string]bool{
		"remote_dev__shell":     true,
		"remote_dev__job_start": true,
	}

	pathTools := 0
	for _, tool := range list.Tools {
		raw, marshalErr := json.Marshal(tool.InputSchema)
		if marshalErr != nil {
			t.Fatalf("encode %s schema: %v", tool.Name, marshalErr)
		}
		var schema map[string]interface{}
		if unmarshalErr := json.Unmarshal(raw, &schema); unmarshalErr != nil {
			t.Fatalf("decode %s schema: %v", tool.Name, unmarshalErr)
		}
		props, _ := schema["properties"].(map[string]interface{})

		hasPath := false
		for key := range props {
			if pathParams[key] {
				hasPath = true
				break
			}
		}
		_, declaresWorkdir := props["workdir"]

		if hasPath && !declaresWorkdir {
			t.Errorf("%s takes a path argument but does not declare workdir", tool.Name)
		}
		if !hasPath && declaresWorkdir && !nativeWorkdir[tool.Name] {
			t.Errorf("%s declares workdir but has no path argument to anchor it", tool.Name)
		}
		if hasPath {
			pathTools++
		}
	}
	if pathTools == 0 {
		t.Fatal("no tool declares a path argument; this guard would pass vacuously")
	}
}
