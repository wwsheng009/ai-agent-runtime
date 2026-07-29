package style

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCustomPaletteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ocean.yaml")
	content := `
name: ocean
variant: dark
roles:
  Tool:
    fg: "#00bcd4"
    bold: true
  Error:
    fg: "#ff5252"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadCustomPaletteFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Name != "ocean" {
		t.Fatalf("name=%q", p.Name)
	}
	if !p.HasAllRequiredRoles() {
		t.Fatal("missing required roles")
	}
	tool := p.StyleFor(RoleTool)
	if !tool.Bold || !tool.Foreground.IsSet() {
		t.Fatalf("tool style: %+v", tool)
	}
}

func TestLoadCustomPalettesFromDirSkipsBad(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "good.yaml"), []byte("name: good\nvariant: dark\nroles: {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: x\nvariant: neon\n"), 0o644)
	pals, diags := LoadCustomPalettesFromDir(dir)
	if len(pals) != 1 || pals[0].Name != "good" {
		t.Fatalf("pals=%+v diags=%v", pals, diags)
	}
	if len(diags) == 0 {
		t.Fatal("expected diagnostic for bad file")
	}
}
