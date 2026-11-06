package remotetools_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// This file audits the remote tool surface from the client's side: for every
// parameter a tool advertises in tools/list, is there code that actually reads
// it?
//
// Why it matters: a parameter that is declared but never read is silently
// ignored at runtime. The caller has no way to tell, so it looks like the tool
// accepted the request. That is the worst shape a tool contract can take.
//
// The audit is layered so each step is inspectable:
//  1. the schema comes from tools/list, not from the source;
//  2. reads come from the AST of one entry function plus the helper functions
//     that receive the argument map;
//  3. anything read through a means the AST cannot follow is listed by name
//     with a reason, so every exception shows up in review.

// scanEntry identifies one function whose body may read arguments. receiver is
// empty for a plain function and required to disambiguate methods that share a
// name (searchcore has both globHandler.Execute and grepHandler.Execute).
type scanEntry struct {
	file     string
	receiver string
	name     string
}

// auditAccessors are the helpers that take an argument key as a string literal.
var auditAccessors = map[string]bool{
	"stringArg": true, "boolArg": true, "intArg": true, "intArgOK": true,
	"stringArgAny": true, "GrepGlobListFromArgs": true,
	"GrepPathListFromArgs": true, "GrepPatternsFromArgs": true,
	"grepTypeListFromArgs": true,
	"argSlice":             true, "argString": true,
}

// scanEntries returns every argument key the given functions read, whether
// through an accessor or by indexing the map with a literal.
func scanEntries(t *testing.T, entries []scanEntry) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		node, err := parser.ParseFile(fset, entry.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.file, err)
		}
		var body *ast.BlockStmt
		ast.Inspect(node, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name != entry.name {
				return true
			}
			if receiverName(fn) != entry.receiver {
				return true
			}
			body = fn.Body
			return false
		})
		if body == nil {
			t.Fatalf("function %s (receiver %q) not found in %s",
				entry.name, entry.receiver, entry.file)
		}
		ast.Inspect(body, func(n ast.Node) bool {
			if index, ok := n.(*ast.IndexExpr); ok {
				if lit, ok := index.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					key := strings.Trim(lit.Value, `"`)
					// Only the argument map counts. `metadata["key"]` is an
					// output field, and mixing the two was how an output key
					// could look like an input the schema forgot to declare.
					if isArgMapExpr(index.X) {
						keys[key] = true
					}
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || !auditAccessors[ident.Name] {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					keys[strings.Trim(lit.Value, `"`)] = true
				}
			}
			return true
		})
	}
	return keys
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch typ := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := typ.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return typ.Name
	}
	return ""
}

// isArgMapExpr reports whether the indexed expression names the argument map,
// either directly (`args[...]`) or through the call's field (`call.Args[...]`).
// Indexing anything else - `metadata`, `entry` inside a files[] element - is not
// an argument read.
func isArgMapExpr(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name == "args"
	case *ast.SelectorExpr:
		return typed.Sel.Name == "Args"
	}
	return false
}

// declaredParams returns the parameters a tool advertises through tools/list.
func declaredParams(t *testing.T, name string) map[string]bool {
	t.Helper()
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
	for _, tool := range list.Tools {
		if tool.Name != name {
			continue
		}
		raw, marshalErr := json.Marshal(tool.InputSchema)
		if marshalErr != nil {
			t.Fatalf("encode %s schema: %v", name, marshalErr)
		}
		var schema map[string]interface{}
		if unmarshalErr := json.Unmarshal(raw, &schema); unmarshalErr != nil {
			t.Fatalf("decode %s schema: %v", name, unmarshalErr)
		}
		props, _ := schema["properties"].(map[string]interface{})
		out := make(map[string]bool, len(props))
		for key := range props {
			out[key] = true
		}
		return out
	}
	t.Fatalf("%s is not registered; every remote tool must be discoverable", name)
	return nil
}

func repoPath(parts ...string) string {
	dir, _ := filepath.Abs(filepath.Join(append([]string{".."}, parts...)...))
	return dir
}

// schemaAudit is one tool's audit configuration.
type schemaAudit struct {
	tool string
	scan []scanEntry
	// indirect names keys the functions read without a literal the AST can see,
	// each with the reason. Every entry is a claim that has to stay true.
	indirect map[string]string
	// filtered names parameters the canonical schema declares and this surface
	// removes with toolcore.FilterSchemaParams because the capability behind them
	// is absent here (view's `symbol` needs a code index a remote host does not
	// have). The handler still reads them: a caller that sends one anyway gets the
	// documented fallback instead of silence.
	filtered map[string]string
}

func searchHandlerPath() string { return repoPath("toolcore", "searchcore", "handler.go") }
func viewHandlerPath() string   { return repoPath("toolcore", "viewcore", "handler.go") }
func lsHandlerPath() string     { return repoPath("toolcore", "lscore", "handler.go") }

// grepAudit covers the search handler. The indirect entries are the keys whose
// reads happen inside helpers that take the whole argument map.
func grepAudit() schemaAudit {
	return schemaAudit{
		tool: "remote_dev__grep",
		scan: []scanEntry{
			{file: searchHandlerPath(), receiver: "grepHandler", name: "Execute"},
			// Helpers that receive the whole argument map. Listing them
			// explicitly keeps the audit honest: every key the handler reads is
			// covered by code the scan actually visits, instead of being
			// asserted in the `indirect` map.
			{file: searchHandlerPath(), name: "grepModeFromArgs"},
			{file: searchHandlerPath(), name: "grepUnrestrictedFromArgs"},
			{file: searchHandlerPath(), name: "grepIgnoreFilesFromArgs"},
			{file: searchHandlerPath(), name: "grepPatternFilesFromArgs"},
			{file: searchHandlerPath(), name: "grepRootsFromArgs"},
			{file: searchHandlerPath(), name: "grepContextFromArgs"},
			{file: searchHandlerPath(), name: "grepMaxFileSizeFromArgs"},
			{file: searchHandlerPath(), name: "grepSortFromArgs"},
			// Presentation-only rg spellings are read here: they change nothing, and
			// the audit still wants their reads covered by code it visits.
			{file: searchHandlerPath(), name: "collectIgnoredPresentationParams"},
		},
		indirect: map[string]string{
			"patterns": "read inside GrepPatternsFromArgs, which takes the whole map",
			"regexp":   "read inside GrepPatternsFromArgs, which takes the whole map",
			"pattern":  "read inside GrepPatternsFromArgs, which takes the whole map",
			"workdir":  "injected by prefixedTool.Parameters and consumed by remoteToolBase.resolve through the per-call context, so this handler never names the key",
		},
	}
}

func globAudit() schemaAudit {
	return schemaAudit{
		tool: "remote_dev__glob",
		scan: []scanEntry{
			{file: searchHandlerPath(), receiver: "globHandler", name: "Execute"},
		},
		indirect: map[string]string{
			"workdir": "injected by prefixedTool.Parameters and consumed by remoteToolBase.resolve through the per-call context, so this handler never names the key",
		},
	}
}

func viewAudit() schemaAudit {
	return schemaAudit{
		tool: "remote_dev__view",
		scan: []scanEntry{
			{file: viewHandlerPath(), receiver: "handler", name: "Execute"},
			{file: viewHandlerPath(), name: "viewRequests"},
			{file: viewHandlerPath(), name: "hasFilesArg"},
		},
		filtered: map[string]string{
			"symbol": "the remote runtime has no code index, so registry.go removes it with FilterSchemaParams(ParamSymbol); the handler resolves it through toolcore.SymbolResolver and falls back to the file window when the surface cannot answer",
		},
		indirect: map[string]string{
			"workdir": "injected by prefixedTool.Parameters and consumed by remoteToolBase.resolve through the per-call context, so this handler never names the key",
		},
	}
}

func lsAudit() schemaAudit {
	return schemaAudit{
		tool: "remote_dev__list_dir",
		scan: []scanEntry{
			{file: lsHandlerPath(), receiver: "handler", name: "Execute"},
		},
		indirect: map[string]string{
			"workdir": "injected by prefixedTool.Parameters and consumed by remoteToolBase.resolve through the per-call context, so this handler never names the key",
		},
	}
}

// TestRemoteToolSchemasOnlyDeclareImplementedParams is the core assertion: a
// parameter offered to clients must be read by the implementation.
func TestRemoteToolSchemasOnlyDeclareImplementedParams(t *testing.T) {
	for _, audit := range []schemaAudit{grepAudit(), globAudit(), viewAudit(), lsAudit()} {
		t.Run(audit.tool, func(t *testing.T) {
			declared := declaredParams(t, audit.tool)
			read := scanEntries(t, audit.scan)

			unread := make([]string, 0, len(declared))
			for key := range declared {
				if read[key] {
					continue
				}
				if reason, ok := audit.indirect[key]; ok && reason != "" {
					continue
				}
				unread = append(unread, key)
			}
			sort.Strings(unread)
			if len(unread) > 0 {
				t.Fatalf("declared to clients but never read: %v\n"+
					"Implement it or drop it from the schema - a parameter that does "+
					"nothing is worse than one that is absent.", unread)
			}
		})
	}
}

// TestRemoteToolSchemasExposeWhatHandlersRead is the other direction: a
// capability the handler understands should be reachable by clients.
func TestRemoteToolSchemasExposeWhatHandlersRead(t *testing.T) {
	for _, audit := range []schemaAudit{grepAudit(), globAudit(), viewAudit(), lsAudit()} {
		t.Run(audit.tool, func(t *testing.T) {
			declared := declaredParams(t, audit.tool)
			read := scanEntries(t, audit.scan)

			hidden := make([]string, 0, len(read))
			for key := range read {
				if declared[key] || audit.indirect[key] != "" {
					continue
				}
				if reason, ok := audit.filtered[key]; ok && reason != "" {
					continue
				}
				hidden = append(hidden, key)
			}
			sort.Strings(hidden)
			if len(hidden) > 0 {
				t.Fatalf("handler reads these but the schema does not declare them: %v", hidden)
			}
		})
	}
}

// TestRemoteGrepSchemaAliasesAreDeclared checks the spellings that exist purely
// as ergonomic synonyms: a caller who finds one in the handler should also see it
// in the schema.
func TestRemoteGrepSchemaAliasesAreDeclared(t *testing.T) {
	declared := declaredParams(t, "remote_dev__grep")
	for _, alias := range []string{"files_without_matches", "file_type", "word_regexp", "case_insensitive"} {
		if !declared[alias] {
			t.Fatalf("handler accepts %q but the schema does not declare it", alias)
		}
	}
}

// TestSchemaAuditDetectsUnreadParam protects the audit itself. A regression here
// would mean the guard silently stopped guarding.
func TestSchemaAuditDetectsUnreadParam(t *testing.T) {
	audit := lsAudit()
	declared := declaredParams(t, audit.tool)
	dl := scanEntries(t, audit.scan)

	// Simulate a schema that promises one more parameter than the code reads.
	declared["pretend_new_option"] = true

	unread := 0
	for key := range declared {
		if dl[key] {
			continue
		}
		if reason, ok := audit.indirect[key]; ok && reason != "" {
			continue
		}
		unread++
	}
	if unread != 1 {
		t.Fatalf("the audit must notice exactly the injected parameter, saw %d", unread)
	}
}

var _ = fmt.Sprintf
