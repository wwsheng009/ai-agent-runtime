package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type codexSkillRootSpec struct {
	Path  string
	Scope string
}

// DiscoverCodexSkillLoadOutcome 扫描 Codex 风格 skills roots，返回 skills 和 errors。
//
// anchor / configFile 的默认 roots 规则与 DiscoverCodexCompatibleSkillDirs 保持一致；
// extraRoots 作为补充的 user scope roots 参与同一轮扫描。
func DiscoverCodexSkillLoadOutcome(anchor, configFile string, extraRoots []string) *CodexSkillLoadOutcome {
	homeDir := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if homeDir == "" {
		homeDir = strings.TrimSpace(os.Getenv("HOME"))
	}
	if homeDir == "" {
		if resolvedHomeDir, err := os.UserHomeDir(); err == nil {
			homeDir = strings.TrimSpace(resolvedHomeDir)
		}
	}

	return discoverCodexSkillLoadOutcome(anchor, configFile, homeDir, extraRoots)
}

func discoverCodexSkillLoadOutcome(anchor, configFile, homeDir string, extraRoots []string) *CodexSkillLoadOutcome {
	outcome := &CodexSkillLoadOutcome{
		Skills: make([]*CodexSkillMetadata, 0, 8),
		Errors: make([]CodexSkillError, 0),
	}

	parser := NewManifestParser()
	parser.SetCompanionPromptLoadMode(CompanionPromptLoadLazy)

	seenRoots := make(map[string]struct{})
	seenSkills := make(map[string]struct{})

	addRoot := func(spec codexSkillRootSpec) {
		spec.Path = canonicalizeSkillTreePath(spec.Path, true)
		if spec.Path == "" {
			return
		}
		if _, exists := seenRoots[spec.Path]; exists {
			return
		}
		seenRoots[spec.Path] = struct{}{}

		followSymlinks := !isCodexSystemSkillRoot(spec.Path)
		if err := walkSkillTree(spec.Path, followSymlinks, func(entry skillTreeEntry) error {
			if entry.Info == nil || entry.Info.IsDir() {
				return nil
			}
			if !isCodexSkillPath(entry.Path) {
				return nil
			}

			skillPath := filepath.Clean(entry.Path)
			if _, exists := seenSkills[skillPath]; exists {
				return nil
			}
			seenSkills[skillPath] = struct{}{}

			summary, err := parser.ParseSummaryFile(skillPath)
			if err != nil {
				outcome.Errors = append(outcome.Errors, CodexSkillError{
					Path:    skillPath,
					Message: err.Error(),
				})
				return nil
			}
			if summary == nil {
				outcome.Errors = append(outcome.Errors, CodexSkillError{
					Path:    skillPath,
					Message: "codex skill summary is nil",
				})
				return nil
			}

			meta := summary.Codex
			if meta == nil {
				meta = &CodexSkillMetadata{
					Name:             summary.Name,
					Description:      summary.Description,
					ShortDescription: summary.ShortDescription,
					PathToSkillsMD:   skillPath,
					MetadataPath:     codexMetadataPathForSkillPath(skillPath),
					Enabled:          true,
				}
			} else {
				meta = meta.CloneWithoutBody()
			}
			if meta == nil {
				outcome.Errors = append(outcome.Errors, CodexSkillError{
					Path:    skillPath,
					Message: "failed to construct codex skill metadata",
				})
				return nil
			}

			if meta.Scope == "" {
				meta.Scope = spec.Scope
			}
			if meta.Scope == "" {
				meta.Scope = CodexSkillScopeUnknown
			}
			if meta.PathToSkillsMD == "" {
				meta.PathToSkillsMD = skillPath
			}
			if meta.MetadataPath == "" {
				meta.MetadataPath = codexMetadataPathForSkillPath(skillPath)
			}
			meta.Enabled = true
			meta.Normalize()
			outcome.Skills = append(outcome.Skills, meta)
			return nil
		}); err != nil {
			outcome.Errors = append(outcome.Errors, CodexSkillError{
				Path:    spec.Path,
				Message: err.Error(),
			})
		}
	}

	for _, spec := range discoverCodexCompatibleSkillRootSpecs(anchor, configFile, homeDir) {
		addRoot(spec)
	}
	for _, root := range normalizeSkillDirs(extraRoots) {
		addRoot(codexSkillRootSpec{
			Path:  root,
			Scope: CodexSkillScopeUser,
		})
	}

	sort.SliceStable(outcome.Skills, func(i, j int) bool {
		left := outcome.Skills[i]
		right := outcome.Skills[j]
		if left == nil || right == nil {
			return left != nil
		}
		if rank := codexScopeRank(left.Scope) - codexScopeRank(right.Scope); rank != 0 {
			return rank < 0
		}
		if left.PathToSkillsMD != right.PathToSkillsMD {
			return left.PathToSkillsMD < right.PathToSkillsMD
		}
		return left.Name < right.Name
	})

	return outcome
}

func codexScopeRank(scope string) int {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case CodexSkillScopeRepo:
		return 0
	case CodexSkillScopeUser:
		return 1
	case CodexSkillScopeSystem:
		return 2
	case CodexSkillScopeAdmin:
		return 3
	case CodexSkillScopeUnknown:
		return 4
	default:
		return 5
	}
}
