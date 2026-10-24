package searchcore

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// grepRenderInputs carries everything the page renderer needs. Rendering is
// part of the shared contract: every surface prints the same shapes, the ones
// the local tool has always printed.
type grepRenderInputs struct {
	paths         toolcore.PathResolver
	result        *GrepResult
	options       GrepOptions
	patterns      []string
	roots         []string
	mode          GrepMode
	contextBefore int
	contextAfter  int
}

type grepPage struct {
	content       string
	contextLines  int
	matchCount    int
	renderedFiles int
}

// displayPathFor renders the file column of one match. A single-file search
// shows only the file name; a directory search shows the workspace-relative
// path; a multi-root search prefixes the entry with the root label the caller
// spelled - all three inherited from the local tool's scope display rules.
func (in grepRenderInputs) displayPathFor(file string) string {
	if in.result != nil && in.result.SingleFile && len(in.roots) == 1 {
		return filepath.Base(file)
	}
	if len(in.roots) > 1 {
		root := closestRoot(in.roots, file)
		if root != "" {
			label := in.paths.Relative(root)
			if label == "" || label == "." {
				return in.paths.Relative(file)
			}
			rel := in.paths.Relative(file)
			if strings.HasPrefix(rel, label+"/") {
				return rel
			}
			return label + "/" + rel
		}
	}
	return in.paths.Relative(file)
}

func closestRoot(roots []string, file string) string {
	best := ""
	for _, root := range roots {
		if root == "" {
			continue
		}
		if strings.HasPrefix(file, root+string(filepath.Separator)) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// renderGrepPage renders one result page in the local tool's output shapes:
//   - content:        `path:line: text` (fragment: `path:line:col: text`)
//   - context lines:  `path>N: text` for matches, `path N: text` for context,
//     blocks separated by `--`
//   - files:          one path per line
//   - count:          `path:count`
//   - empty page:     未找到匹配的内容
//   - page window:    中文分页提示 with next_offset (never the word 截断)
func renderGrepPage(in grepRenderInputs) grepPage {
	page := grepPage{matchCount: len(in.result.Matches)}
	var lines []string

	switch {
	case in.mode == GrepModeFiles:
		for _, entry := range in.result.Files {
			lines = append(lines, in.displayPathFor(entry.File))
		}
		page.renderedFiles = len(in.result.Files)
	case in.mode == GrepModeCount:
		for _, entry := range in.result.Files {
			lines = append(lines, fmt.Sprintf("%s:%d", in.displayPathFor(entry.File), entry.Count))
		}
		page.renderedFiles = len(in.result.Files)
	case in.contextBefore > 0 || in.contextAfter > 0:
		expanded := ExpandGrepContext(in.result.Matches, in.contextBefore, in.contextAfter)
		page.contextLines = len(expanded)
		printedAny := false
		for _, line := range expanded {
			if line.GroupStart && printedAny {
				lines = append(lines, "--")
			}
			display := in.displayPathFor(line.File)
			if line.IsMatch {
				lines = append(lines, fmt.Sprintf("%s>%d: %s", display, line.Line, line.Text))
			} else {
				lines = append(lines, fmt.Sprintf("%s %d: %s", display, line.Line, line.Text))
			}
			printedAny = true
		}
	default:
		for _, item := range in.result.Matches {
			if item.Column > 0 {
				lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", in.displayPathFor(item.File), item.Line, item.Column, item.Text))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d: %s", in.displayPathFor(item.File), item.Line, item.Text))
		}
	}

	content := strings.Join(lines, "\n")
	if content == "" {
		content = "未找到匹配的内容"
	}
	if in.result.Truncated {
		nextOffset := in.options.Offset + len(lines)
		if in.options.Offset > 0 || page.renderedFiles == 0 {
			content += fmt.Sprintf("\n\n(已分页显示第 %d-%d 个匹配；next_offset=%d 可继续分页)", in.options.Offset+1, nextOffset, nextOffset)
		} else {
			content += fmt.Sprintf("\n\n(已分页返回前 %d 个匹配；next_offset=%d 可继续分页读取后续匹配)", len(lines), nextOffset)
		}
	}
	page.content = content
	return page
}
