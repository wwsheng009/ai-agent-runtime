package imagegen

import "testing"

func TestValidateGenerateRequest_AcceptsValidGPTImage2Payload(t *testing.T) {
	compression := 75
	req := &GenerateRequest{
		Model:             "gpt-image-2",
		Prompt:            "an orange cat on a window sill",
		N:                 2,
		Size:              "1024x1024",
		Quality:           "medium",
		Background:        "auto",
		OutputFormat:      "png",
		OutputCompression: &compression,
	}

	if err := Validate(req); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if req.OutputFormat != "png" {
		t.Fatalf("expected normalized output format png, got %q", req.OutputFormat)
	}
}

func TestValidateGenerateRequest_RejectsTransparentBackgroundForGPTImage2(t *testing.T) {
	req := &GenerateRequest{
		Model:        "gpt-image-2",
		Prompt:       "transparent cat",
		N:            1,
		Size:         "1024x1024",
		Quality:      "medium",
		Background:   "transparent",
		OutputFormat: "png",
	}

	if err := Validate(req); err == nil {
		t.Fatal("expected transparent background to be rejected for gpt-image-2")
	}
}

func TestValidateGenerateRequest_RejectsInvalidGPTImage2Size(t *testing.T) {
	req := &GenerateRequest{
		Model:        "gpt-image-2",
		Prompt:       "bad size",
		N:            1,
		Size:         "1000x1000",
		Quality:      "medium",
		Background:   "auto",
		OutputFormat: "png",
	}

	if err := Validate(req); err == nil {
		t.Fatal("expected invalid gpt-image-2 size to be rejected")
	}
}

func TestValidateGenerateRequest_RejectsLegacySizeForOlderModel(t *testing.T) {
	req := &GenerateRequest{
		Model:        "gpt-image-1.5",
		Prompt:       "bad legacy size",
		N:            1,
		Size:         "800x800",
		Quality:      "medium",
		Background:   "auto",
		OutputFormat: "png",
	}

	if err := Validate(req); err == nil {
		t.Fatal("expected invalid legacy size to be rejected")
	}
}

func TestNormalizeGenerateRequest_DefaultsAndAliases(t *testing.T) {
	req := &GenerateRequest{
		Model:        "",
		Prompt:       "  hello  ",
		OutputFormat: "jpg",
	}

	NormalizeGenerateRequest(req)
	if req.Model != DefaultModel {
		t.Fatalf("unexpected default model: %q", req.Model)
	}
	if req.Size != DefaultSize {
		t.Fatalf("unexpected default size: %q", req.Size)
	}
	if req.Quality != DefaultQuality {
		t.Fatalf("unexpected default quality: %q", req.Quality)
	}
	if req.OutputFormat != "jpeg" {
		t.Fatalf("expected jpg to normalize to jpeg, got %q", req.OutputFormat)
	}
}
