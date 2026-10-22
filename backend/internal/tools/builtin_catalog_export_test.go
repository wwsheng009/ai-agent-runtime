package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuiltinToolkitCatalogIsACopy pins that callers cannot mutate the
// process-wide construction table through the exported view.
func TestBuiltinToolkitCatalogIsACopy(t *testing.T) {
	first := BuiltinToolkitCatalog()
	require.NotEmpty(t, first)

	original := first[0]
	first[0].Canonical = "mutated"
	first[0].New = nil

	second := BuiltinToolkitCatalog()
	require.Equal(t, original.Canonical, second[0].Canonical,
		"exported catalog must not alias the internal table")
	require.NotNil(t, second[0].New, "exported catalog must keep constructors")
}

// TestBuiltinToolkitCatalogKeepsCanonicalAliases pins the alias rows that make
// compatibility spellings routable (bash/execute_shell_command/read/rg).
func TestBuiltinToolkitCatalogKeepsCanonicalAliases(t *testing.T) {
	rows := map[string]int{}
	for _, spec := range BuiltinToolkitCatalog() {
		rows[spec.Canonical]++
	}
	for _, canonical := range []string{"shell", "view", "grep"} {
		require.GreaterOrEqual(t, rows[canonical], 2,
			"canonical %q must keep its alias instance rows", canonical)
	}
}
