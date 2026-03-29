package chatcore

import (
	"reflect"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestCatalogTracksBuiltinAndSkillEntries(t *testing.T) {
	catalog := NewCatalog()
	catalog.Upsert(CatalogEntry{
		Name:   "builtin__diagnose",
		Schema: testCatalogSchema("builtin__diagnose"),
	})
	catalog.Upsert(CatalogEntry{
		Name:    "skill__alpha",
		IsSkill: true,
		Schema:  testCatalogSchema("skill__alpha"),
	})

	stats := catalog.Stats()
	if stats.TotalFunctions != 2 {
		t.Fatalf("expected 2 functions, got %d", stats.TotalFunctions)
	}
	if stats.BuiltinTools != 1 {
		t.Fatalf("expected 1 builtin tool, got %d", stats.BuiltinTools)
	}
	if stats.SkillFunctions != 1 {
		t.Fatalf("expected 1 skill function, got %d", stats.SkillFunctions)
	}

	builtinSchemas := catalog.BuiltinSchemas()
	if len(builtinSchemas) != 1 {
		t.Fatalf("expected 1 builtin schema, got %v", builtinSchemas)
	}
	if builtinSchemas[0]["name"] != "builtin__diagnose" {
		t.Fatalf("expected builtin__diagnose schema, got %v", builtinSchemas[0]["name"])
	}

	if schema := catalog.SkillSchema("skill__alpha"); schema["name"] != "skill__alpha" {
		t.Fatalf("expected skill__alpha schema, got %v", schema["name"])
	}
}

func TestCatalogSelectRespectsSkillExposureModes(t *testing.T) {
	catalog := NewCatalog()
	catalog.Upsert(CatalogEntry{
		Name:   "builtin__diagnose",
		Schema: testCatalogSchema("builtin__diagnose"),
	})
	catalog.Upsert(CatalogEntry{
		Name:    "skill__alpha",
		IsSkill: true,
		Schema:  testCatalogSchema("skill__alpha"),
	})

	testCases := []struct {
		name             string
		mode             string
		exposedSkills    map[string]struct{}
		includeBuiltin   bool
		builtinFunctions []string
		skillFunctions   []string
		finalFunctions   []string
	}{
		{
			name:             "auto includes builtin when no skill matches",
			mode:             SkillExposureAuto,
			includeBuiltin:   true,
			builtinFunctions: []string{"builtin__diagnose"},
			finalFunctions:   []string{"builtin__diagnose"},
		},
		{
			name:           "prefer suppresses builtin when a skill matches",
			mode:           SkillExposurePrefer,
			exposedSkills:  map[string]struct{}{"skill__alpha": {}},
			includeBuiltin: false,
			skillFunctions: []string{"skill__alpha"},
			finalFunctions: []string{"skill__alpha"},
		},
		{
			name:           "only exposes matched skills",
			mode:           SkillExposureOnly,
			exposedSkills:  map[string]struct{}{"skill__alpha": {}},
			includeBuiltin: false,
			skillFunctions: []string{"skill__alpha"},
			finalFunctions: []string{"skill__alpha"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			selection := catalog.Select(SelectionOptions{
				ExposureMode:  tc.mode,
				ExposedSkills: tc.exposedSkills,
			})
			if selection == nil {
				t.Fatal("expected selection")
			}
			if selection.Mode != tc.mode {
				t.Fatalf("expected mode %q, got %q", tc.mode, selection.Mode)
			}
			if selection.IncludeBuiltin != tc.includeBuiltin {
				t.Fatalf("expected include_builtin=%t, got %t", tc.includeBuiltin, selection.IncludeBuiltin)
			}
			if !reflect.DeepEqual(selection.BuiltinFunctions, tc.builtinFunctions) {
				t.Fatalf("unexpected builtin functions: got %v want %v", selection.BuiltinFunctions, tc.builtinFunctions)
			}
			if !reflect.DeepEqual(selection.SkillFunctions, tc.skillFunctions) {
				t.Fatalf("unexpected skill functions: got %v want %v", selection.SkillFunctions, tc.skillFunctions)
			}
			if !reflect.DeepEqual(selection.FinalFunctionNames, tc.finalFunctions) {
				t.Fatalf("unexpected final functions: got %v want %v", selection.FinalFunctionNames, tc.finalFunctions)
			}
		})
	}
}

func TestCatalogSelectAppliesToolPolicyOnlyToBuiltinEntries(t *testing.T) {
	catalog := NewCatalog()
	catalog.Upsert(CatalogEntry{
		Name:   "write_file",
		Schema: testCatalogSchema("write_file"),
	})
	catalog.Upsert(CatalogEntry{
		Name:   "read_file",
		Schema: testCatalogSchema("read_file"),
	})
	catalog.Upsert(CatalogEntry{
		Name:    "skill__alpha",
		IsSkill: true,
		Schema:  testCatalogSchema("skill__alpha"),
	})

	selection := catalog.Select(SelectionOptions{
		ExposureMode: SkillExposureAuto,
		ExposedSkills: map[string]struct{}{
			"skill__alpha": {},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"read_file"}, false),
	})
	if selection == nil {
		t.Fatal("expected selection")
	}
	if !reflect.DeepEqual(selection.BuiltinFunctions, []string{"read_file"}) {
		t.Fatalf("expected only read_file builtin exposure, got %v", selection.BuiltinFunctions)
	}
	if !reflect.DeepEqual(selection.SkillFunctions, []string{"skill__alpha"}) {
		t.Fatalf("expected skill__alpha skill exposure, got %v", selection.SkillFunctions)
	}
	if !reflect.DeepEqual(selection.FinalFunctionNames, []string{"read_file", "skill__alpha"}) {
		t.Fatalf("unexpected final functions: %v", selection.FinalFunctionNames)
	}
}

func TestCatalogSelectReturnsSchemasInDeterministicOrder(t *testing.T) {
	catalog := NewCatalog()
	catalog.Upsert(CatalogEntry{
		Name:   "write_file",
		Schema: testCatalogSchema("write_file"),
	})
	catalog.Upsert(CatalogEntry{
		Name:   "read_file",
		Schema: testCatalogSchema("read_file"),
	})
	catalog.Upsert(CatalogEntry{
		Name:    "skill__beta",
		IsSkill: true,
		Schema:  testCatalogSchema("skill__beta"),
	})
	catalog.Upsert(CatalogEntry{
		Name:    "skill__alpha",
		IsSkill: true,
		Schema:  testCatalogSchema("skill__alpha"),
	})

	selection := catalog.Select(SelectionOptions{
		ExposureMode: SkillExposureAuto,
		ExposedSkills: map[string]struct{}{
			"skill__beta":  {},
			"skill__alpha": {},
		},
	})
	if selection == nil {
		t.Fatal("expected selection")
	}

	gotNames := make([]string, 0, len(selection.Schemas))
	for _, schema := range selection.Schemas {
		gotNames = append(gotNames, schema["name"].(string))
	}

	wantNames := []string{"read_file", "write_file", "skill__alpha", "skill__beta"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("unexpected schema order: got %v want %v", gotNames, wantNames)
	}
}

func testCatalogSchema(name string) map[string]interface{} {
	return map[string]interface{}{
		"name":        name,
		"description": name + " description",
		"parameters": map[string]interface{}{
			"type": "object",
		},
	}
}
