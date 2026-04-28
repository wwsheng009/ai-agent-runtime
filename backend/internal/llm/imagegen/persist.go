package imagegen

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
		ByteCount:  len(decoded),
	}, nil
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
