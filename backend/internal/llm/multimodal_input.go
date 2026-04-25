package llm

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const MetadataKeyInputImages = "input_images"

// LocalInputImage stores one locally resolved input image attachment.
type LocalInputImage struct {
	Path         string `json:"path,omitempty"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Source       string `json:"source,omitempty"`
}

// PersistLocalInputImages copies local input images to a session artifact
// directory so that the session remains self-contained even if the original
// files are moved or deleted. It updates the ResolvedPath fields in the
// message metadata to point to the persisted copies. If artifactDir is empty,
// no persistence is performed.
func PersistLocalInputImages(message *types.Message, artifactDir string) error {
	if message == nil || message.Metadata == nil || artifactDir == "" {
		return nil
	}
	images := ExtractLocalInputImages(mapFromMetadata(message.Metadata))
	if len(images) == 0 {
		return nil
	}

	// Ensure artifact directory exists
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return fmt.Errorf("create image artifact dir %q: %w", artifactDir, err)
	}

	updated := make([]LocalInputImage, 0, len(images))
	for _, image := range images {
		srcPath := strings.TrimSpace(image.ResolvedPath)
		if srcPath == "" {
			srcPath = strings.TrimSpace(image.Path)
		}
		if srcPath == "" {
			updated = append(updated, image)
			continue
		}

		// Read the source file
		srcBytes, err := os.ReadFile(srcPath)
		if err != nil {
			// If we can't read the original, keep the image as-is
			updated = append(updated, image)
			continue
		}

		// Generate a stable filename
		ext := filepath.Ext(srcPath)
		if ext == "" {
			ext = mimeExtensionFromLocalInputImageMimeType(image.MimeType)
		}
		filename := fmt.Sprintf("%s%s", stableImageArtifactName(srcPath), ext)
		dstPath := filepath.Join(artifactDir, filename)

		// Write the copy (atomic-ish via tmp + rename)
		tmpPath := dstPath + ".tmp"
		if err := os.WriteFile(tmpPath, srcBytes, 0o644); err != nil {
			updated = append(updated, image)
			continue
		}
		if err := os.Rename(tmpPath, dstPath); err != nil {
			os.Remove(tmpPath)
			updated = append(updated, image)
			continue
		}

		// Update the resolved path to the persisted copy
		persisted := image
		persisted.ResolvedPath = dstPath
		persisted.Source = image.Source + "+persisted"
		updated = append(updated, persisted)
	}

	message.Metadata.Set(MetadataKeyInputImages, encodeLocalInputImages(updated))
	return nil
}

// stableImageArtifactName generates a short, stable name for an image file
// based on its original path to avoid duplicates.
func stableImageArtifactName(originalPath string) string {
	h := fnv32(originalPath)
	return fmt.Sprintf("img_%08x", h)
}

func fnv32(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for _, c := range s {
		h ^= uint32(c)
		h *= prime32
	}
	return h
}

func mimeExtensionFromLocalInputImageMimeType(mimeType string) string {
	switch sanitizeLocalInputImageMimeType(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

var (
	localPromptImageMarkdownPattern    = regexp.MustCompile(`!\[[^\]]*\]\(([^)\n]+)\)`)
	localPromptImageBacktickPattern    = regexp.MustCompile("`([^`\n]+)`")
	localPromptImageDoubleQuotePattern = regexp.MustCompile(`"([^"\n]+)"`)
	localPromptImageSingleQuotePattern = regexp.MustCompile(`'([^'\n]+)'`)
)

// NewUserPromptMessage creates a user message and automatically upgrades local
// image paths mentioned in the prompt into structured image metadata.
func NewUserPromptMessage(prompt string) *types.Message {
	message, err := NewUserPromptMessageWithImages(prompt, nil)
	if err != nil || message == nil {
		return types.NewUserMessage(prompt)
	}
	return message
}

// NewUserPromptMessageWithImages creates a user message and merges explicit
// local image attachments with prompt-detected local image paths.
func NewUserPromptMessageWithImages(prompt string, explicitPaths []string) (*types.Message, error) {
	message := types.NewUserMessage(prompt)
	images, err := ResolveLocalInputImages(prompt, explicitPaths)
	if err != nil {
		return nil, err
	}
	if len(images) > 0 {
		message.Metadata.Set(MetadataKeyInputImages, encodeLocalInputImages(images))
		// Populate structured ContentParts for first-class multimodal access
		parts := make([]types.ContentPart, 0, 1+len(images))
		parts = append(parts, types.ContentPart{Type: types.ContentPartText, Text: prompt})
		for _, image := range images {
			dataURL, dataErr := localInputImageDataURL(image)
			part := types.ContentPart{
				Type:     types.ContentPartImage,
				MimeType: image.MimeType,
				Path:     image.Path,
				Source:   image.Source,
			}
			if dataErr == nil {
				part.ImageURL = dataURL
			}
			parts = append(parts, part)
		}
		message.ContentParts = parts
	}
	return message, nil
}

// ResolveLocalInputImages resolves prompt-detected and explicit local image
// paths into normalized attachment metadata.
func ResolveLocalInputImages(prompt string, explicitPaths []string) ([]LocalInputImage, error) {
	result := make([]LocalInputImage, 0)
	seen := make(map[string]struct{})

	for _, rawPath := range explicitPaths {
		image, err := resolveLocalInputImage(rawPath, "explicit", true)
		if err != nil {
			return nil, err
		}
		appendUniqueLocalInputImage(&result, seen, image)
	}

	for _, candidate := range detectPromptLocalImagePathCandidates(prompt) {
		image, err := resolveLocalInputImage(candidate, "prompt", false)
		if err != nil || image == nil {
			continue
		}
		appendUniqueLocalInputImage(&result, seen, image)
	}

	return result, nil
}

// ExtractLocalInputImages decodes normalized local image metadata from a
// message metadata map.
func ExtractLocalInputImages(metadata map[string]interface{}) []LocalInputImage {
	if len(metadata) == 0 {
		return nil
	}
	items := decodeSliceOfMaps(metadata[MetadataKeyInputImages])
	if len(items) == 0 {
		return nil
	}

	result := make([]LocalInputImage, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		image := LocalInputImage{
			Path:         strings.TrimSpace(stringValue(item["path"])),
			ResolvedPath: strings.TrimSpace(stringValue(item["resolved_path"])),
			MimeType:     sanitizeLocalInputImageMimeType(stringValue(item["mime_type"])),
			Source:       strings.TrimSpace(stringValue(item["source"])),
		}
		if image.Path == "" && image.ResolvedPath == "" {
			continue
		}
		result = append(result, image)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// MessageHasLocalInputImages reports whether the message contains normalized
// local image attachment metadata.
func MessageHasLocalInputImages(message *types.Message) bool {
	if message == nil {
		return false
	}
	return len(ExtractLocalInputImages(mapFromMetadata(message.Metadata))) > 0
}

// ValidateLocalInputImagePaths checks explicit image paths and returns
// human-readable warning strings for any that are missing or not valid images.
// This does NOT return errors; it returns warnings suitable for user display.
func ValidateLocalInputImagePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	var warnings []string
	for _, rawPath := range paths {
		rawPath = strings.TrimSpace(rawPath)
		if rawPath == "" {
			continue
		}
		resolved, err := resolveAbsoluteLocalInputPath(rawPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("image path %q could not be resolved: %v", rawPath, err))
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("image path %q not found: %v", rawPath, err))
			continue
		}
		if info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("image path %q is a directory, not a file", rawPath))
			continue
		}
		mimeType, err := detectLocalInputImageMimeType(resolved)
		if err != nil || !isSupportedLocalInputImageMimeType(mimeType) {
			warnings = append(warnings, fmt.Sprintf("image path %q is not a supported image type", rawPath))
			continue
		}
	}
	return warnings
}

func detectPromptLocalImagePathCandidates(prompt string) []string {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}

	candidates := make([]string, 0)
	seen := make(map[string]struct{})
	appendCandidate := func(value string) {
		value = trimLocalInputCandidatePath(value)
		if !looksLikeLocalImagePathCandidate(value) {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}

	for _, pattern := range []*regexp.Regexp{
		localPromptImageMarkdownPattern,
		localPromptImageBacktickPattern,
		localPromptImageDoubleQuotePattern,
		localPromptImageSingleQuotePattern,
	} {
		for _, match := range pattern.FindAllStringSubmatch(prompt, -1) {
			if len(match) > 1 {
				appendCandidate(match[1])
			}
		}
	}

	for _, field := range strings.Fields(prompt) {
		appendCandidate(field)
	}

	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

func trimLocalInputCandidatePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	for {
		trimmed := strings.TrimSpace(path)
		trimmed = strings.Trim(trimmed, "\"'`")
		trimmed = strings.TrimPrefix(trimmed, "<")
		trimmed = strings.TrimSuffix(trimmed, ">")
		trimmed = strings.TrimLeft(trimmed, "([{")
		trimmed = strings.TrimRight(trimmed, ",.;:!?)]}")
		trimmed = strings.Trim(trimmed, "\"'`")
		if trimmed == path {
			return trimmed
		}
		path = trimmed
	}
}

func looksLikeLocalImagePathCandidate(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:image/") {
		return false
	}
	if strings.HasPrefix(lower, "file://") {
		return true
	}
	if filepath.IsAbs(path) {
		return true
	}
	if strings.HasPrefix(path, "~") || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") || strings.HasPrefix(path, ".\\") || strings.HasPrefix(path, "..\\") {
		return true
	}
	if strings.ContainsAny(path, `/\\`) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	return strings.HasPrefix(sanitizeLocalInputImageMimeType(mime.TypeByExtension(ext)), "image/")
}

func resolveLocalInputImage(rawPath string, source string, strict bool) (*LocalInputImage, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		if strict {
			return nil, fmt.Errorf("image path is empty")
		}
		return nil, nil
	}

	resolvedPath, err := resolveAbsoluteLocalInputPath(rawPath)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("resolve image path %q: %w", rawPath, err)
		}
		return nil, nil
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("stat image path %q: %w", rawPath, err)
		}
		return nil, nil
	}
	if info.IsDir() {
		if strict {
			return nil, fmt.Errorf("image path %q is a directory", rawPath)
		}
		return nil, nil
	}

	mimeType, err := detectLocalInputImageMimeType(resolvedPath)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("inspect image path %q: %w", rawPath, err)
		}
		return nil, nil
	}
	if !isSupportedLocalInputImageMimeType(mimeType) {
		if strict {
			return nil, fmt.Errorf("unsupported image type for %q: %s", rawPath, mimeType)
		}
		return nil, nil
	}

	return &LocalInputImage{
		Path:         rawPath,
		ResolvedPath: filepath.Clean(resolvedPath),
		MimeType:     mimeType,
		Source:       source,
	}, nil
}

func resolveAbsoluteLocalInputPath(path string) (string, error) {
	path = trimLocalInputCandidatePath(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if decoded := decodeLocalFileURI(path); decoded != "" {
		path = decoded
	}
	if expanded, ok := expandLocalInputTilde(path); ok {
		path = expanded
	}
	resolved, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func decodeLocalFileURI(raw string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "file://") {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		decodedPath = parsed.Path
	}
	decodedPath = strings.TrimSpace(decodedPath)
	if decodedPath == "" {
		return ""
	}
	if len(decodedPath) >= 3 && decodedPath[0] == '/' && decodedPath[2] == ':' {
		decodedPath = decodedPath[1:]
	}
	return decodedPath
}

func expandLocalInputTilde(path string) (string, bool) {
	if path == "" || path[0] != '~' {
		return "", false
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return "", false
	}
	suffix := strings.TrimPrefix(path, "~")
	suffix = strings.TrimPrefix(suffix, "/")
	suffix = strings.TrimPrefix(suffix, `\`)
	if suffix == "" {
		return homeDir, true
	}
	return filepath.Join(homeDir, filepath.FromSlash(suffix)), true
}

func detectLocalInputImageMimeType(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	header := make([]byte, 512)
	count, readErr := file.Read(header)
	if readErr != nil && readErr != io.EOF {
		return "", readErr
	}

	detected := sanitizeLocalInputImageMimeType(http.DetectContentType(header[:count]))
	if isSupportedLocalInputImageMimeType(detected) {
		return detected, nil
	}

	if guessed := sanitizeLocalInputImageMimeType(mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))); isSupportedLocalInputImageMimeType(guessed) {
		return guessed, nil
	}

	if detected == "" {
		detected = "unknown"
	}
	return detected, fmt.Errorf("detected content type %q", detected)
}

func sanitizeLocalInputImageMimeType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err == nil && strings.TrimSpace(mediaType) != "" {
		return strings.ToLower(strings.TrimSpace(mediaType))
	}
	return strings.ToLower(value)
}

func isSupportedLocalInputImageMimeType(mimeType string) bool {
	mimeType = sanitizeLocalInputImageMimeType(mimeType)
	return strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml"
}

func appendUniqueLocalInputImage(images *[]LocalInputImage, seen map[string]struct{}, image *LocalInputImage) {
	if images == nil || seen == nil || image == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(image.ResolvedPath))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(image.Path))
	}
	if key == "" {
		return
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*images = append(*images, *image)
}

func encodeLocalInputImages(images []LocalInputImage) []map[string]interface{} {
	if len(images) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(images))
	for _, image := range images {
		item := map[string]interface{}{}
		if text := strings.TrimSpace(image.Path); text != "" {
			item["path"] = text
		}
		if text := strings.TrimSpace(image.ResolvedPath); text != "" {
			item["resolved_path"] = text
		}
		if text := sanitizeLocalInputImageMimeType(image.MimeType); text != "" {
			item["mime_type"] = text
		}
		if text := strings.TrimSpace(image.Source); text != "" {
			item["source"] = text
		}
		if len(item) == 0 {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func localInputImageDataURL(image LocalInputImage) (string, error) {
	path := strings.TrimSpace(image.ResolvedPath)
	if path == "" {
		path = strings.TrimSpace(image.Path)
	}
	if path == "" {
		return "", fmt.Errorf("image path is empty")
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mimeType := sanitizeLocalInputImageMimeType(image.MimeType)
	if !isSupportedLocalInputImageMimeType(mimeType) {
		mimeType, err = detectLocalInputImageMimeType(path)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(bytes)), nil
}
