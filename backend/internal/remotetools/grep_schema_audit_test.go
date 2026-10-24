package remotetools_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// indirectArgKeys are schema keys the handler reads without passing the literal
// to one of the named accessors - it indexes the argument map directly, or the
// lookup happens inside a helper that takes the whole map. The static scan below
// cannot see those, so they are declared here with the reason.
//
// Every entry is a claim that has to stay true: if the handler stops reading one
// of these, the audit still passes, so keep the list short and review it when
// the handler changes.
var indirectArgKeys = map[string]string{
	"mode":         "read by grepModeFromArgs via args[\"mode\"]",
	"max_filesize": "read by grepMaxFileSizeFromArgs via args[\"max_filesize\"] (string or integer)",
	"patterns":     "read inside GrepPatternsFromArgs, which takes the whole map",
	"regexp":       "read inside GrepPatternsFromArgs, which takes the whole map",
	"pattern":      "read inside GrepPatternsFromArgs, which takes the whole map",
}

// handlerReadKeys scans the shared handler package for argument names the
// implementation actually consumes. It exists to catch the failure mode where a
// parameter is declared to clients but nothing ever reads it.
func handlerReadKeys(t *testing.T) map[string]bool {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "toolcore", "searchcore"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	accessors := map[string]bool{
		"stringArg": true, "boolArg": true, "intArg": true, "intArgOK": true,
		"stringArgAny": true, "GrepGlobListFromArgs": true,
		"GrepPathListFromArgs": true, "GrepPatternsFromArgs": true,
	}
	keys := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, parseErr := parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", file, parseErr)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || !accessors[ident.Name] {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				keys[strings.Trim(lit.Value, `"`)] = true
			}
			return true
		})
	}
	if len(keys) == 0 {
		t.Fatal("scan found no argument keys; the accessor list is probably stale")
	}
	return keys
}

// declaredGrepParams returns the parameter names the remote schema exposes to
// clients, as seen through tools/list.
func declaredGrepParams(t *testing.T) map[string]bool {
	t.Helper()
	server, err := remotetools.NewServer(newTestConfig(t, t.TempDir()), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range result.Tools {
		if tool.Name != "remote_dev__grep" {
			continue
		}
		raw, marshalErr := json.Marshal(tool.InputSchema)
		if marshalErr != nil {
			t.Fatalf("encode schema: %v", marshalErr)
		}
		var schema map[string]interface{}
		if unmarshalErr := json.Unmarshal(raw, &schema); unmarshalErr != nil {
			t.Fatalf("decode schema: %v", unmarshalErr)
		}
		props, _ := schema["properties"].(map[string]interface{})
		out := make(map[string]bool, len(props))
		for key := range props {
			out[key] = true
		}
		return out
	}
	t.Fatal("remote_dev__grep is not registered")
	return nil
}

// TestRemoteGrepSchemaEveryParamIsRead is the guard against a schema that
// promises more than the implementation delivers. A declared parameter that no
// code reads is invisible to the caller and silently ignored by the runtime -
// the worst shape a tool contract can have.
func TestRemoteGrepSchemaEveryParamIsRead(t *testing.T) {
	declared := declaredGrepParams(t)
	read := handlerReadKeys(t)

	unread := make([]string, 0, len(declared))
	for key := range declared {
		if read[key] {
			continue
		}
		if reason, indirect := indirectArgKeys[key]; indirect && reason != "" {
			continue
		}
		unread = append(unread, key)
	}
	sort.Strings(unread)
	if len(unread) > 0 {
		t.Fatalf("declared to clients but never read by the handler: %v\n"+
			"Either implement it or remove it from the schema - a parameter that "+
			"does nothing is worse than one that is absent.", unread)
	}
}

// TestRemoteGrepSchemaMatchesLocalCoreSet keeps the two surfaces aligned: every
// parameter the shared handler understands should be reachable remotely, unless
// it is deliberately local-only.
func TestRemoteGrepSchemaMatchesLocalCoreSet(t *testing.T) {
	declared := declaredGrepParams(t)
	read := handlerReadKeys(t)

	notExposed := make([]string, 0, len(read))
	for key := range read {
		if declared[key] || indirectArgKeys[key] != "" {
			continue
		}
		notExposed = append(notExposed, key)
	}
	sort.Strings(notExposed)
	if len(notExposed) > 0 {
		t.Fatalf("the shared handler reads these parameters but the remote schema "+
			"does not declare them: %v", notExposed)
	}
}

// TestRemoteGrepSchemaHasNoUnknownAlias is the other direction of the alias
// check: every spelling accepted by the handler should be discoverable.
func TestRemoteGrepSchemaAliasesAreDeclared(t *testing.T) {
	declared := declaredGrepParams(t)
	// Aliases the handler accepts as synonyms of a declared parameter.
	aliases := []string{"files_without_matches", "file_type", "word_regexp"}
	for _, alias := range aliases {
		if !declared[alias] {
			t.Fatalf("handler accepts %q but the schema does not declare it", alias)
		}
	}
}
