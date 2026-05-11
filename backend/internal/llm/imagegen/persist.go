package imagegen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SavedImage stores local metadata for one saved generated image result.
type SavedImage struct {
	ID            string `json:"id,omitempty"`
	Status        string `json:"status,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	SavedPath     string `json:"saved_path,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	ByteCount     int    `json:"byte_count,omitempty"`
}

// SaveBase64Image decodes the provided base64 payload and writes it into
// outputDir using a sanitized id hint. The file name is deterministic on first
// write and gains a timestamp suffix only when a collision already exists.
func SaveBase64Image(outputDir, idHint, b64, format string) (SavedImage, error) {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return SavedImage{}, fmt.Errorf("generated image output dir is empty")
	}
	rawID := strings.TrimSpace(idHint)
	if rawID == "" {
		rawID = "generated_image"
	}
	fileID := sanitizeImageID(rawID)
	payload := strings.TrimSpace(b64)
	if payload == "" {
		return SavedImage{}, fmt.Errorf("generated image %s returned empty payload", rawID)
	}
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		return SavedImage{}, fmt.Errorf("generated image %s returned unsupported data URL payload", rawID)
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return SavedImage{}, fmt.Errorf("generated image %s returned invalid base64 payload: %w", rawID, err)
	}

	outputFormat, err := normalizeOutputFormatForPersist(format)
	if err != nil {
		return SavedImage{}, err
	}
	mimeType := mimeTypeForFormat(outputFormat)

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return SavedImage{}, fmt.Errorf("create generated image directory: %w", err)
	}

	path, err := chooseGeneratedImagePath(outputDir, fileID, outputFormat)
	if err != nil {
		return SavedImage{}, err
	}
	if err := os.WriteFile(path, decoded, 0o644); err != nil {
		return SavedImage{}, fmt.Errorf("write generated image %s: %w", rawID, err)
	}

	sum := sha256.Sum256(decoded)
	return SavedImage{
		ID:        rawID,
		MimeType:  mimeType,
		SavedPath: path,
		SHA256:    hex.EncodeToString(sum[:]),
		ByteCount: len(decoded),
	}, nil
}

// SaveURLImage downloads the image from the given URL and writes it into
// outputDir using a sanitized id hint. The file format is inferred from
// the URL path or Content-Type header, falling back to the requested format.
func SaveURLImage(ctx context.Context, outputDir, idHint, imageURL, format string, httpClient *http.Client) (SavedImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return SavedImage{}, fmt.Errorf("generated image output dir is empty")
	}
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return SavedImage{}, fmt.Errorf("image URL is empty")
	}

	rawID := strings.TrimSpace(idHint)
	if rawID == "" {
		rawID = "generated_image"
	}
	fileID := sanitizeImageID(rawID)

	outputFormat, err := normalizeOutputFormatForPersist(format)
	if err != nil {
		// Fall back to inferring from URL or using png.
		outputFormat = "png"
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return SavedImage{}, fmt.Errorf("create generated image directory: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return SavedImage{}, fmt.Errorf("create download request for %s: %w", rawID, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return SavedImage{}, fmt.Errorf("download image %s from %s: %w", rawID, imageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SavedImage{}, fmt.Errorf("download image %s: HTTP %d", rawID, resp.StatusCode)
	}

	// Infer format from Content-Type if the requested format was empty/default.
	if format == "" {
		if inferred := formatFromContentType(resp.Header.Get("Content-Type")); inferred != "" {
			outputFormat = inferred
		}
	}

	mimeType := mimeTypeForFormat(outputFormat)
	path, err := chooseGeneratedImagePath(outputDir, fileID, outputFormat)
	if err != nil {
		return SavedImage{}, err
	}

	// Download to temp file first, then rename atomically.
	tmpFile, err := os.CreateTemp(outputDir, fileID+"_*.tmp")
	if err != nil {
		return SavedImage{}, fmt.Errorf("create temp file for %s: %w", rawID, err)
	}
	tmpPath := tmpFile.Name()

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmpFile, hasher), resp.Body)
	closeErr := tmpFile.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return SavedImage{}, fmt.Errorf("download image %s: %w", rawID, copyErr)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return SavedImage{}, fmt.Errorf("close temp file for %s: %w", rawID, closeErr)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return SavedImage{}, fmt.Errorf("rename temp file for %s: %w", rawID, err)
	}

	return SavedImage{
		ID:        rawID,
		MimeType:  mimeType,
		SavedPath: path,
		SHA256:    hex.EncodeToString(hasher.Sum(nil)),
		ByteCount: int(written),
	}, nil
}

func formatFromContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	// Strip parameters (e.g. "image/png; charset=utf-8")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	switch strings.ToLower(contentType) {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/png":
		return "png"
	default:
		return ""
	}
}

func chooseGeneratedImagePath(outputDir, idHint, format string) (string, error) {
	base := filepath.Join(outputDir, idHint+"."+format)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base, nil
	} else if err != nil {
		return "", err
	}

	stamp := time.Now().UTC().UnixNano()
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := filepath.Join(outputDir, fmt.Sprintf("%s_%d_%d.%s", idHint, stamp, attempt, format))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("unable to allocate unique generated image file name for %s", idHint)
}

func normalizeOutputFormatForPersist(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "png":
		return "png", nil
	case "jpeg", "jpg":
		return "jpeg", nil
	case "webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("output_format must be png, jpeg, or webp")
	}
}

func mimeTypeForFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func sanitizeImageID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "generated_image"
	}
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-' || ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}
	result := builder.String()
	if result == "" {
		return "generated_image"
	}
	return result
}
