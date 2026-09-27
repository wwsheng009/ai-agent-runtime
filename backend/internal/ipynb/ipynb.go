// Package ipynb renders Jupyter notebooks as tagged Markdown so the view tool
// can page them with its normal window semantics (analysis §3.10).
//
// A notebook is JSON: markdown and code cells become `# %% [...] cell N`
// sections, outputs stay attached to their cell, image outputs are decoded for
// the image passthrough channel, and very large text outputs are folded into
// a jq pointer instead of flooding the context.
package ipynb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/imageprep"
)

// maxOutputChars is the fold threshold for a single text output. Above it the
// renderer emits a jq pointer; the raw text stays in the file for shell/jq.
const maxOutputChars = 10000

// maxImageBytes bounds one decoded image output; larger payloads are counted
// as skipped instead of being held in memory. Variables so tests can shrink
// the budget without allocating megabytes.
var maxImageBytes = 32 << 20

// maxTotalImageBytes bounds the decoded images of one notebook: the per-image
// cap alone would let a notebook with dozens of outputs accumulate unbounded
// memory.
var maxTotalImageBytes = 64 << 20

// maxCells bounds rendering work for pathological notebooks.
const maxCells = 5000

// Image is one decoded image output, addressed by its absolute cell/output
// index so the caller can point the model back at the source path.
type Image struct {
	Cell   int
	Output int
	MIME   string
	Data   []byte
	// Line is the 0-based line of Render.Markdown that carries this image's
	// placeholder, so a windowed reader can attach only the images it actually
	// delivered instead of the whole notebook's (2026-09-27 review).
	Line int
}

// Render is the Markdown projection plus structured metadata.
type Render struct {
	Markdown string
	Images   []Image
	Metadata map[string]interface{}

	imageBytes int
}

type notebook struct {
	NBFormat int    `json:"nbformat"`
	Cells    []cell `json:"cells"`
}

type cell struct {
	CellType string   `json:"cell_type"`
	Source   rawText  `json:"source"`
	Outputs  []output `json:"outputs"`
}

type output struct {
	OutputType string                     `json:"output_type"`
	Name       string                     `json:"name"`
	Text       rawText                    `json:"text"`
	Data       map[string]json.RawMessage `json:"data"`
	EName      string                     `json:"ename"`
	EValue     string                     `json:"evalue"`
}

// rawText accepts the two nbformat spellings of text: a string or an array of
// line strings.
type rawText struct {
	text string
	set  bool
}

func (r *rawText) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	r.set = true
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		r.text = single
		return nil
	}
	var lines []string
	if err := json.Unmarshal(data, &lines); err != nil {
		return fmt.Errorf("notebook text must be a string or string array: %w", err)
	}
	r.text = strings.Join(lines, "")
	return nil
}

// RenderFile parses path and returns its Markdown projection.
func RenderFile(path string) (Render, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Render{}, err
	}
	return RenderBytes(raw)
}

// RenderBytes parses notebook JSON. It is exported so tests and future
// non-file sources can share the renderer.
func RenderBytes(raw []byte) (Render, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	var doc notebook
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Render{}, fmt.Errorf("notebook JSON 解析失败: %w", err)
	}
	if doc.Cells == nil {
		return Render{}, fmt.Errorf("不是有效的 notebook: 缺少 cells 字段")
	}

	render := Render{Metadata: map[string]interface{}{}}
	if doc.NBFormat > 0 {
		render.Metadata["notebook_format"] = doc.NBFormat
	}
	if len(doc.Cells) > maxCells {
		render.Metadata["notebook_cells_omitted"] = len(doc.Cells) - maxCells
		doc.Cells = doc.Cells[:maxCells]
	}

	var builder strings.Builder
	codeCells, markdownCells, omitted := 0, 0, 0
	for index, cell := range doc.Cells {
		label := strings.TrimSpace(cell.CellType)
		switch label {
		case "markdown":
			markdownCells++
		case "code":
			codeCells++
		default:
			if label == "" {
				label = "raw"
			}
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "# %%%% [%s] cell %d\n", label, index+1)

		source := cell.Source.text
		switch cell.CellType {
		case "markdown", "raw":
			if source != "" {
				builder.WriteString(source)
				if !strings.HasSuffix(source, "\n") {
					builder.WriteString("\n")
				}
			}
		case "code":
			fence := notebookFenceFor(source)
			fmt.Fprintf(&builder, "%spython\n", fence)
			builder.WriteString(source)
			if source != "" && !strings.HasSuffix(source, "\n") {
				builder.WriteString("\n")
			}
			fmt.Fprintf(&builder, "%s\n", fence)
			omitted += renderOutputs(&builder, &render, index, cell.Outputs)
		}
	}

	render.Markdown = builder.String()
	render.Metadata["cells"] = len(doc.Cells)
	render.Metadata["code_cells"] = codeCells
	render.Metadata["markdown_cells"] = markdownCells
	render.Metadata["images"] = len(render.Images)
	render.Metadata["outputs_omitted"] = omitted
	return render, nil
}

// notebookFenceFor returns a code fence that no backtick run inside source can
// close. The previous loop grew the fence one character at a time and re-scanned
// the whole source on every iteration, so a cell with a long backtick run cost
// Θ(N²) time and allocations while the notebook itself stayed far below the file
// size cap (2026-09-27 review: 4 KiB of backticks allocated ~9 MB, 8 KiB ~36 MB).
func notebookFenceFor(source string) string {
	longest := 0
	run := 0
	for i := 0; i < len(source); i++ {
		if source[i] != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	if longest < 3 {
		return "```"
	}
	// A fence of length L is closable by the source exactly when the source
	// contains a backtick run of at least L; longest+1 is therefore minimal and
	// sufficient.
	return strings.Repeat("`", longest+1)
}

func renderOutputs(builder *strings.Builder, render *Render, cellIndex int, outputs []output) int {
	omitted := 0
	for outputIndex, out := range outputs {
		switch out.OutputType {
		case "stream", "execute_result", "display_data", "update_display_data":
			text, pointer := outputTextAndPointer(out, cellIndex, outputIndex)
			if strings.TrimSpace(text) == "" && !hasImage(out) {
				// A display output can carry only representations this renderer
				// cannot attach (image/svg+xml is the common one). Dropping it
				// silently hid the output entirely, with doc_degraded left false
				// (2026-09-27 review): leave a recovery pointer and count it.
				if mime := unsupportedImageMIME(out); mime != "" {
					fmt.Fprintf(builder, "# output omitted: {\"jq\": %q, \"note\": \"unsupported image MIME %s; use shell/jq to inspect\"}\n",
						pointer, mime)
					omitted++
				}
				continue
			}
			if utf8.RuneCountInString(text) > maxOutputChars {
				fmt.Fprintf(builder, "# output omitted: {\"jq\": %q, \"note\": \"output omitted; use shell/jq to inspect\"}\n",
					pointer)
				omitted++
			} else if strings.TrimSpace(text) != "" {
				fmt.Fprintf(builder, "# output (%s):\n%s", outputLabel(out), text)
				if !strings.HasSuffix(text, "\n") {
					builder.WriteString("\n")
				}
			}
			omitted += appendImages(builder, render, cellIndex, outputIndex, out)
		case "error":
			message := strings.TrimSpace(strings.Join([]string{out.EName, out.EValue}, ": "))
			message = truncateRunes(message, 300)
			fmt.Fprintf(builder, "# error: %s\n", message)
		}
	}
	return omitted
}

// outputTextMIMEKeys is the preference order for textual representations; the
// same order drives the text and its jq pointer so a folded output can never
// point at a different (or empty) field.
var outputTextMIMEKeys = []string{"text/plain", "text/markdown", "text/html"}

// outputTextAndPointer returns the first non-empty textual representation and
// the jq address of exactly that field.
func outputTextAndPointer(out output, cellIndex, outputIndex int) (string, string) {
	base := fmt.Sprintf(".cells[%d].outputs[%d]", cellIndex, outputIndex)
	if out.Text.set && out.Text.text != "" {
		return out.Text.text, base + ".text"
	}
	for _, key := range outputTextMIMEKeys {
		raw, ok := out.Data[key]
		if !ok {
			continue
		}
		var text rawText
		if err := text.UnmarshalJSON(raw); err == nil && text.text != "" {
			return text.text, fmt.Sprintf("%s.data[%q]", base, key)
		}
	}
	return "", base
}

// truncateRunes cuts by characters, not bytes, so a long CJK error message
// cannot end in an invalid UTF-8 fragment.
func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func outputLabel(out output) string {
	if out.OutputType == "stream" {
		if strings.TrimSpace(out.Name) != "" {
			return out.Name
		}
		return "stdout"
	}
	return "result"
}

func hasImage(out output) bool {
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif"} {
		if _, ok := out.Data[mime]; ok {
			return true
		}
	}
	return false
}

// unsupportedImageMIME names the first image representation this renderer cannot
// attach, so the omitted-output note can say why the output disappeared.
func unsupportedImageMIME(out output) string {
	if len(out.Data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(out.Data))
	for key := range out.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasPrefix(key, "image/") && !supportedImageMIME(key) {
			return key
		}
	}
	return ""
}

func supportedImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
		return true
	}
	return false
}

func appendImages(builder *strings.Builder, render *Render, cellIndex, outputIndex int, out output) int {
	skipped := 0
	// Placeholder line numbers are needed for the window filter, but rescanning
	// the whole markdown prefix for every image made a many-image notebook
	// quadratic (2026-09-27 review H18). Every write below emits exactly one
	// line, so counting the writes in this call is equivalent.
	line := strings.Count(builder.String(), "\n")
	lineOf := func() int { return line }
	emit := func(format string, args ...interface{}) {
		fmt.Fprintf(builder, format, args...)
		line++
	}
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif"} {
		raw, ok := out.Data[mime]
		if !ok {
			continue
		}
		// 先按 base64 长度估算解码后体积：超限时不必真正解码（解码结果本来
		// 就要丢弃，先解码等于白白分配一块大内存）。
		if encoded := encodedTextLength(raw); encoded > 0 && base64.StdEncoding.DecodedLen(encoded) > maxImageBytes {
			emit("# [image] cell %d output %d: %s (超过 %d 字节上限未附加)\n",
				cellIndex+1, outputIndex, mime, maxImageBytes)
			skipped++
			continue
		}
		data, err := decodeBase64Output(raw)
		if err != nil {
			emit("# [image] cell %d output %d: %s (base64 解码失败)\n", cellIndex+1, outputIndex, mime)
			skipped++
			continue
		}
		if len(data) > maxImageBytes {
			emit("# [image] cell %d output %d: %s (%d bytes, 超过 %d 字节上限未附加)\n",
				cellIndex+1, outputIndex, mime, len(data), maxImageBytes)
			skipped++
			continue
		}
		if err := imageprep.ValidateImageData(data, mime); err != nil {
			emit("# [image] cell %d output %d: %s (不是完整图片，未附加: %v)\n",
				cellIndex+1, outputIndex, mime, err)
			skipped++
			continue
		}
		if render.imageBytes+len(data) > maxTotalImageBytes {
			emit("# [image] cell %d output %d: %s (notebook 图片总量超过 %d 字节上限未附加)\n",
				cellIndex+1, outputIndex, mime, maxTotalImageBytes)
			skipped++
			continue
		}
		render.imageBytes += len(data)
		render.Images = append(render.Images, Image{
			Cell:   cellIndex,
			Output: outputIndex,
			MIME:   mime,
			Data:   data,
			Line:   lineOf(),
		})
		emit("# [image] cell %d output %d: %s (%d bytes)\n", cellIndex+1, outputIndex, mime, len(data))
	}
	return skipped
}

// encodedTextLength reports the string length of a JSON string / line-array
// value without decoding it.
func encodedTextLength(raw json.RawMessage) int {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return len(strings.TrimSpace(single))
	}
	var text rawText
	if err := text.UnmarshalJSON(raw); err == nil {
		return len(strings.TrimSpace(text.text))
	}
	return 0
}

func decodeBase64Output(raw json.RawMessage) ([]byte, error) {
	var text rawText
	if err := text.UnmarshalJSON(raw); err == nil && text.text != "" {
		return base64.StdEncoding.DecodeString(strings.TrimSpace(text.text))
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
}
