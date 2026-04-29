package skill

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManifestParser_ParsesCopiedCodexSystemSkills(t *testing.T) {
	repoRoot := repoRootForCodexSkillParseTests(t)
	skillDir := filepath.Join(repoRoot, ".agents", "skills")
	creatorPath := filepath.Join(skillDir, "skill-creator", "SKILL.md")
	installerPath := filepath.Join(skillDir, "skill-installer", "SKILL.md")

	parser := NewManifestParser()

	skills, err := parser.ParseDir(skillDir)
	require.NoError(t, err)
	require.NotEmpty(t, skills)

	creator := findSkillBySourcePath(skills, creatorPath)
	require.NotNil(t, creator)
	require.NotNil(t, creator.Source)
	require.Equal(t, SkillSourceFormatCodex, creator.Source.Format)
	require.Equal(t, creatorPath, creator.Source.Path)
	require.NotEmpty(t, creator.Name)
	require.NotEmpty(t, creator.Description)
	require.NotNil(t, creator.Codex)

	installer := findSkillBySourcePath(skills, installerPath)
	require.NotNil(t, installer)
	require.NotNil(t, installer.Source)
	require.Equal(t, SkillSourceFormatCodex, installer.Source.Format)
	require.Equal(t, installerPath, installer.Source.Path)
	require.NotEmpty(t, installer.Name)
	require.NotEmpty(t, installer.Description)
	require.NotNil(t, installer.Codex)

	summaries, err := parser.ParseSummaryDir(skillDir)
	require.NoError(t, err)
	require.NotEmpty(t, summaries)

	creatorSummary := findSummaryBySourcePath(summaries, creatorPath)
	require.NotNil(t, creatorSummary)
	require.NotNil(t, creatorSummary.Source)
	require.Equal(t, SkillSourceFormatCodex, creatorSummary.Source.Format)
	require.Equal(t, creatorPath, creatorSummary.Source.Path)

	installerSummary := findSummaryBySourcePath(summaries, installerPath)
	require.NotNil(t, installerSummary)
	require.NotNil(t, installerSummary.Source)
	require.Equal(t, SkillSourceFormatCodex, installerSummary.Source.Format)
	require.Equal(t, installerPath, installerSummary.Source.Path)
}

func repoRootForCodexSkillParseTests(t *testing.T) string {
	t.Helper()

	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
}

func findSkillBySourcePath(skills []*Skill, sourcePath string) *Skill {
	sourcePath = filepath.Clean(sourcePath)
	for _, skill := range skills {
		if skill == nil || skill.Source == nil {
			continue
		}
		if filepath.Clean(skill.Source.Path) == sourcePath {
			return skill
		}
	}
	return nil
}

func findSummaryBySourcePath(summaries []*SkillSummary, sourcePath string) *SkillSummary {
	sourcePath = filepath.Clean(sourcePath)
	for _, summary := range summaries {
		if summary == nil || summary.Source == nil {
			continue
		}
		if filepath.Clean(summary.Source.Path) == sourcePath {
			return summary
		}
	}
	return nil
}
