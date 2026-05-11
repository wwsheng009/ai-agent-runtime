package imagegen

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

const (
	DefaultModel        = "gpt-image-2"
	DefaultSize         = "auto"
	DefaultQuality      = "medium"
	DefaultOutputFormat = "png"
)

const (
	GPTImageModelPrefix = "gpt-image-"
	GPTImage2Model      = "gpt-image-2"
)

var (
	AllowedLegacySizes   = map[string]struct{}{"1024x1024": {}, "1536x1024": {}, "1024x1536": {}, "auto": {}}
	AllowedQualities     = map[string]struct{}{"low": {}, "medium": {}, "high": {}, "auto": {}}
	AllowedBackgrounds   = map[string]struct{}{"transparent": {}, "opaque": {}, "auto": {}, "": {}}
	AllowedOutputFormats = map[string]struct{}{"png": {}, "jpeg": {}, "webp": {}, "jpg": {}}
	sizePattern          = regexp.MustCompile(`^([1-9][0-9]*)x([1-9][0-9]*)$`)
)

// GenerateRequest describes an image generations request for the OpenAI
// compatible /v1/images/generations endpoint.
type GenerateRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 int    `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	// Provider is a client-side hint indicating which provider to use.
	// It is NOT serialized in the API request (json:"-").
	Provider string `json:"-"`
}

// GenerateResponse mirrors the endpoint response payload.
type GenerateResponse struct {
	Created int                    `json:"created"`
	Data    []GenerateResponseItem `json:"data"`
}

// GenerateResponseItem describes one returned image artifact.
type GenerateResponseItem struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// HasB64JSON reports whether the item contains a base64-encoded image.
func (item *GenerateResponseItem) HasB64JSON() bool {
	return strings.TrimSpace(item.B64JSON) != ""
}

// HasURL reports whether the item contains a downloadable image URL.
func (item *GenerateResponseItem) HasURL() bool {
	return strings.TrimSpace(item.URL) != ""
}

// IsGPTImageModel reports whether the model name refers to a GPT Image model
// (e.g. gpt-image-1, gpt-image-2).
func IsGPTImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), GPTImageModelPrefix)
}

// NormalizeGenerateRequest applies endpoint-friendly defaults and canonical
// casing to the provided request in-place.
func NormalizeGenerateRequest(req *GenerateRequest) {
	if req == nil {
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Size = strings.TrimSpace(req.Size)
	req.Quality = strings.ToLower(strings.TrimSpace(req.Quality))
	req.Background = strings.ToLower(strings.TrimSpace(req.Background))
	req.OutputFormat = normalizeOutputFormat(strings.TrimSpace(req.OutputFormat))
	req.Moderation = strings.ToLower(strings.TrimSpace(req.Moderation))

	if req.Model == "" {
		req.Model = DefaultModel
	}
	if req.N == 0 {
		req.N = 1
	}
	// Size, quality, and output_format defaults are GPT-Image-specific.
	// For non-GPT-Image models, leave them empty so the upstream API
	// applies its own defaults rather than receiving an incompatible value.
	if IsGPTImageModel(req.Model) {
		if req.Size == "" {
			req.Size = DefaultSize
		}
		if req.Quality == "" {
			req.Quality = DefaultQuality
		}
		if req.OutputFormat == "" {
			req.OutputFormat = DefaultOutputFormat
		}
	}
}

// Validate checks whether req is acceptable for the generations endpoint.
func Validate(req *GenerateRequest) error {
	if req == nil {
		return fmt.Errorf("generate request is nil")
	}
	NormalizeGenerateRequest(req)

	if req.Prompt == "" {
		return fmt.Errorf("prompt cannot be empty")
	}
	if req.Model == "" {
		return fmt.Errorf("model cannot be empty")
	}
	if req.N < 1 || req.N > 10 {
		return fmt.Errorf("n must be between 1 and 10")
	}
	if req.Quality != "" {
		if _, ok := AllowedQualities[req.Quality]; !ok {
			return fmt.Errorf("quality must be one of low, medium, high, or auto")
		}
	}
	if _, ok := AllowedBackgrounds[req.Background]; !ok {
		return fmt.Errorf("background must be one of transparent, opaque, or auto")
	}
	if req.OutputFormat != "" {
		if _, ok := AllowedOutputFormats[req.OutputFormat]; !ok {
			return fmt.Errorf("output_format must be png, jpeg, or webp")
		}
		if req.OutputFormat == "jpg" {
			req.OutputFormat = "jpeg"
		}
	}
	if req.Background == "transparent" && req.OutputFormat != "" && req.OutputFormat != "png" && req.OutputFormat != "webp" {
		return fmt.Errorf("transparent background requires output-format png or webp")
	}
	if req.OutputCompression != nil {
		if *req.OutputCompression < 0 || *req.OutputCompression > 100 {
			return fmt.Errorf("output_compression must be between 0 and 100")
		}
	}
	// GPT Image specific validation only applies to gpt-image-* models.
	if strings.HasPrefix(strings.ToLower(req.Model), GPTImageModelPrefix) {
		if err := validateGPTImageModel(req); err != nil {
			return err
		}
	}
	return nil
}

// validateGPTImageModel applies GPT Image model-specific constraints.
func validateGPTImageModel(req *GenerateRequest) error {
	if req.Quality == "" {
		return fmt.Errorf("quality must be one of low, medium, high, or auto")
	}
	if req.OutputFormat == "" {
		return fmt.Errorf("output_format must be png, jpeg, or webp")
	}
	switch strings.ToLower(strings.TrimSpace(req.Model)) {
	case GPTImage2Model:
		if req.Background == "transparent" {
			return fmt.Errorf("transparent backgrounds are not supported in %s", GPTImage2Model)
		}
		if err := validateGPTImage2Size(req.Size); err != nil {
			return err
		}
	default:
		if _, ok := AllowedLegacySizes[strings.ToLower(strings.TrimSpace(req.Size))]; !ok {
			return fmt.Errorf("size must be one of 1024x1024, 1536x1024, 1024x1536, or auto for this GPT Image model")
		}
	}
	return nil
}

func normalizeOutputFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "jpg":
		return "jpeg"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateGPTImage2Size(size string) error {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "auto" {
		return nil
	}
	match := sizePattern.FindStringSubmatch(size)
	if len(match) != 3 {
		return fmt.Errorf("size must be auto or WIDTHxHEIGHT, for example 1024x1024")
	}
	width, height := parsePositiveInt(match[1]), parsePositiveInt(match[2])
	if width <= 0 || height <= 0 {
		return fmt.Errorf("size must contain positive dimensions")
	}
	maxEdge := maxInt(width, height)
	minEdge := minInt(width, height)
	totalPixels := width * height
	if maxEdge > 3840 {
		return fmt.Errorf("gpt-image-2 size maximum edge length must be less than or equal to 3840px")
	}
	if width%16 != 0 || height%16 != 0 {
		return fmt.Errorf("gpt-image-2 size width and height must be multiples of 16px")
	}
	if float64(maxEdge)/float64(minEdge) > 3.0 {
		return fmt.Errorf("gpt-image-2 size long edge to short edge ratio must not exceed 3:1")
	}
	if totalPixels < 655360 || totalPixels > 8294400 {
		return fmt.Errorf("gpt-image-2 size total pixels must be at least 655,360 and no more than 8,294,400")
	}
	return nil
}

func parsePositiveInt(value string) int {
	if value == "" {
		return 0
	}
	var result int
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0
		}
		result = result*10 + int(ch-'0')
		if result > math.MaxInt32 {
			return 0
		}
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
