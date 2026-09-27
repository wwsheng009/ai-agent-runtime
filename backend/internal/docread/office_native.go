package docread

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Native office container extraction (2026-09-27 review H13/H14).
//
// LibreOffice Impress has no TXT export filter, so `soffice --convert-to txt`
// can never succeed for pptx/odp — installing a converter does not fix that
// route. Both OOXML and ODF are zip+XML, so slide text and workbook sheet names
// are extracted here with the standard library, deterministically and without a
// converter.

// extractPresentationText renders the slide text of a pptx/odp file.
func extractPresentationText(path, kind string) (string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("docread: open %s: %w", filepath.Base(path), err)
	}
	defer archive.Close()
	switch kind {
	case "pptx":
		return extractPPTXText(archive)
	case "odp":
		return extractODPText(archive)
	}
	return "", fmt.Errorf("docread: unsupported presentation kind %q", kind)
}

func extractPPTXText(archive *zip.ReadCloser) (string, error) {
	type slideFile struct {
		number int
		file   *zip.File
	}
	slides := make([]slideFile, 0, 16)
	for _, entry := range archive.File {
		name := strings.ToLower(entry.Name)
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		digits := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
		number, err := strconv.Atoi(digits)
		if err != nil {
			continue
		}
		slides = append(slides, slideFile{number: number, file: entry})
	}
	if len(slides) == 0 {
		return "", fmt.Errorf("docread: pptx contains no slides")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].number < slides[j].number })

	var out strings.Builder
	for index, slide := range slides {
		text, err := extractDrawingMLText(slide.file)
		if err != nil {
			return "", fmt.Errorf("docread: read %s: %w", slide.file.Name, err)
		}
		fmt.Fprintf(&out, "## Slide %d\n%s\n\n", index+1, strings.TrimSpace(text))
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// extractDrawingMLText collects <a:t> runs, breaking lines at </a:p>.
func extractDrawingMLText(entry *zip.File) (string, error) {
	reader, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(reader)
	var out strings.Builder
	inRun := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return out.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "t" {
				inRun = true
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inRun = false
			case "p":
				out.WriteString("\n")
			}
		case xml.CharData:
			if inRun {
				out.Write(typed)
			}
		}
	}
}

func extractODPText(archive *zip.ReadCloser) (string, error) {
	content := findZipEntry(archive, "content.xml")
	if content == nil {
		return "", fmt.Errorf("docread: odp contains no content.xml")
	}
	reader, err := content.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()

	decoder := xml.NewDecoder(reader)
	var out strings.Builder
	slide := 0
	inPage := false
	inParagraph := false
	pageDepth := 0
	paragraphDepth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "page" && strings.Contains(typed.Name.Space, "drawing") {
				slide++
				inPage = true
				pageDepth = 1
				if slide > 1 {
					out.WriteString("\n")
				}
				fmt.Fprintf(&out, "## Slide %d\n", slide)
				continue
			}
			if inPage {
				pageDepth++
				if typed.Name.Local == "p" && strings.Contains(typed.Name.Space, "text") {
					inParagraph = true
					paragraphDepth = 1
				} else if inParagraph {
					paragraphDepth++
				}
			}
		case xml.EndElement:
			if !inPage {
				continue
			}
			if inParagraph && typed.Name.Local == "p" && paragraphDepth == 1 {
				inParagraph = false
				paragraphDepth = 0
				out.WriteString("\n")
			} else if inParagraph {
				paragraphDepth--
			}
			pageDepth--
			if pageDepth == 0 {
				inPage = false
			}
		case xml.CharData:
			if inParagraph {
				out.Write(typed)
			}
		}
	}
	if slide == 0 {
		return "", fmt.Errorf("docread: odp contains no slides")
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// declaredSheetNames lists workbook sheet names in document order; the list is
// used to disclose sheets a CSV conversion did not deliver instead of silently
// dropping them (H14). Missing/unparsable containers yield nil.
func declaredSheetNames(path, kind string) []string {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer archive.Close()
	switch kind {
	case "xlsx":
		if entry := findZipEntry(archive, "xl/workbook.xml"); entry != nil {
			if names := xmlAttributeValues(entry, "sheet", "name", nil); len(names) > 0 {
				return names
			}
		}
	case "ods":
		if entry := findZipEntry(archive, "content.xml"); entry != nil {
			return xmlAttributeValues(entry, "table", "name", func(space string) bool {
				return strings.Contains(space, "opendocument") && strings.Contains(space, "table")
			})
		}
	}
	return nil
}

func findZipEntry(archive *zip.ReadCloser, name string) *zip.File {
	for _, entry := range archive.File {
		if strings.EqualFold(entry.Name, name) {
			return entry
		}
	}
	return nil
}

// xmlAttributeValues collects attr (matched element local name, optional
// namespace predicate) across one XML part, in document order.
func xmlAttributeValues(entry *zip.File, elementLocal, attrLocal string, matchSpace func(string) bool) []string {
	reader, err := entry.Open()
	if err != nil {
		return nil
	}
	defer reader.Close()
	decoder := xml.NewDecoder(reader)
	values := make([]string, 0, 8)
	for {
		token, err := decoder.Token()
		if err != nil {
			return values
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != elementLocal {
			continue
		}
		if matchSpace != nil && !matchSpace(start.Name.Space) {
			continue
		}
		for _, attribute := range start.Attr {
			if attribute.Name.Local == attrLocal {
				if value := strings.TrimSpace(attribute.Value); value != "" {
					values = append(values, value)
				}
				break
			}
		}
	}
}
