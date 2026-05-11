package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRunImageGenerateCommandSavesImage(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]interface{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		var decoded map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		body = decoded
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"created": 123,
			"data": []map[string]interface{}{
				{
					"b64_json":       base64.StdEncoding.EncodeToString([]byte("image-bytes")),
					"revised_prompt": "revised robot",
				},
			},
		})
	}))
	defer server.Close()

	restoreGlobalConfig(t)
	outputDir := t.TempDir()
	result, _, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:    imageCommandTestConfig(server.URL),
		Prompt:    "draw a robot",
		OutputDir: outputDir,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("runImageGenerateCommand failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if result.OutputDir != outputDir {
		t.Fatalf("unexpected output dir: %q", result.OutputDir)
	}
	if !strings.Contains(result.Output, "Generated image saved to") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	images, ok := result.Images.([]map[string]interface{})
	if !ok || len(images) != 1 {
		t.Fatalf("unexpected images metadata: %#v", result.Images)
	}
	savedPath, _ := images[0]["saved_path"].(string)
	if savedPath == "" {
		t.Fatalf("missing saved_path in %#v", images[0])
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("expected image to be saved at %s: %v", savedPath, err)
	}

	mu.Lock()
	captured := body
	mu.Unlock()
	if captured["prompt"] != "draw a robot" {
		t.Fatalf("unexpected prompt: %#v", captured["prompt"])
	}
	if captured["model"] != "gpt-image-2" {
		t.Fatalf("unexpected model: %#v", captured["model"])
	}
}

func TestRunImageGenerateCommandPassesOptionalArgs(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]interface{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		body = decoded
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"b64_json": base64.StdEncoding.EncodeToString([]byte("image-bytes"))},
			},
		})
	}))
	defer server.Close()

	restoreGlobalConfig(t)
	compression := 42
	result, _, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:            imageCommandTestConfig(server.URL),
		Prompt:            "draw a poster",
		Provider:          "SENSENOVA_IMAGE",
		Model:             "sensenova-u1-fast",
		N:                 2,
		Size:              "2752x1536",
		Quality:           "high",
		Background:        "opaque",
		OutputFormat:      "webp",
		OutputCompression: &compression,
		OutputDir:         t.TempDir(),
		Timeout:           5 * time.Second,
	})
	if err != nil {
		t.Fatalf("runImageGenerateCommand failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}

	mu.Lock()
	captured := body
	mu.Unlock()
	assertJSONValue(t, captured, "model", "sensenova-u1-fast")
	assertJSONValue(t, captured, "prompt", "draw a poster")
	assertJSONValue(t, captured, "n", float64(2))
	assertJSONValue(t, captured, "size", "2752x1536")
	assertJSONValue(t, captured, "quality", "high")
	assertJSONValue(t, captured, "background", "opaque")
	assertJSONValue(t, captured, "output_format", "webp")
	assertJSONValue(t, captured, "output_compression", float64(42))
}

func TestRunImageGenerateCommandWritesDebugToWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"b64_json": base64.StdEncoding.EncodeToString([]byte("image-bytes"))},
			},
		})
	}))
	defer server.Close()

	restoreGlobalConfig(t)
	var debug bytes.Buffer
	result, _, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:      imageCommandTestConfig(server.URL),
		Prompt:      "secret prompt should not appear",
		Provider:    "SENSENOVA_IMAGE",
		OutputDir:   t.TempDir(),
		Timeout:     5 * time.Second,
		Debug:       true,
		DebugWriter: &debug,
	})
	if err != nil {
		t.Fatalf("runImageGenerateCommand failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	output := debug.String()
	for _, want := range []string{
		"[image-debug] start",
		"params",
		"provider=\"SENSENOVA_IMAGE\"",
		"debug=\"true\"",
		"calling tool=openai_image_generate debug=true",
		"[image-debug/tool] select",
		"[image-debug/tool] attempt=1/1 provider=\"SENSENOVA_IMAGE\"",
		"[image-debug/tool] attempt=1 api_call",
		"result success=true",
		"saved_path=",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("debug output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "secret prompt should not appear") {
		t.Fatalf("debug output should not include raw prompt:\n%s", output)
	}
}

func TestRunImageGenerateCommandDebugFormatsMissingMetadata(t *testing.T) {
	restoreGlobalConfig(t)
	var debug bytes.Buffer
	result, _, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:      imageCommandTestConfig("http://127.0.0.1"),
		Prompt:      "draw",
		Provider:    "__missing__",
		OutputDir:   t.TempDir(),
		Debug:       true,
		DebugWriter: &debug,
	})
	if err == nil {
		t.Fatalf("expected missing provider error, got result=%+v", result)
	}
	output := debug.String()
	if strings.Contains(output, "%!q") || strings.Contains(output, "<nil>") {
		t.Fatalf("debug output contains formatting artifact:\n%s", output)
	}
	if !strings.Contains(output, `result success=false provider="" model=""`) {
		t.Fatalf("expected empty provider/model debug fields, got:\n%s", output)
	}
}

func TestRunImageGenerateCommandRequiresPrompt(t *testing.T) {
	restoreGlobalConfig(t)
	result, details, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:    imageCommandTestConfig("http://127.0.0.1"),
		OutputDir: filepath.Join(t.TempDir(), "images"),
	})
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected prompt error, got result=%+v details=%+v err=%v", result, details, err)
	}
}

func TestRunImageGenerateCommandRejectsNegativeN(t *testing.T) {
	restoreGlobalConfig(t)
	result, details, err := runImageGenerateCommand(imageGenerateCommandRequest{
		Config:    imageCommandTestConfig("http://127.0.0.1"),
		Prompt:    "draw",
		N:         -1,
		OutputDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "n must be zero or positive") {
		t.Fatalf("expected n error, got result=%+v details=%+v err=%v", result, details, err)
	}
}

func TestImagePromptFromFlags(t *testing.T) {
	cmd := NewImageCommand(nil)
	if got := imagePromptFromFlags(cmd, []string{"draw", "a", "cat"}); got != "draw a cat" {
		t.Fatalf("unexpected positional prompt: %q", got)
	}
	if err := cmd.Flags().Set("prompt", "flag prompt"); err != nil {
		t.Fatalf("set prompt flag: %v", err)
	}
	if got := imagePromptFromFlags(cmd, []string{"positional"}); got != "flag prompt" {
		t.Fatalf("expected prompt flag to win, got %q", got)
	}
}

func TestImageCommandCompressionFlagChanged(t *testing.T) {
	cmd := NewImageCommand(nil)
	if flagChanged(cmd, "output-compression") {
		t.Fatal("output-compression should not be marked changed by default")
	}
	if err := cmd.Flags().Set("output-compression", "0"); err != nil {
		t.Fatalf("set output-compression flag: %v", err)
	}
	if !flagChanged(cmd, "output-compression") {
		t.Fatal("output-compression should be marked changed after explicit set")
	}
}

func TestImageGenerateCommandParamsIncludesNonDefaultPath(t *testing.T) {
	params := imageGenerateCommandParams(imageGenerateCommandRequest{
		Path: "codex_native",
	}, "draw a robot")

	if got := params["path"]; got != "codex_native" {
		t.Fatalf("expected path param, got %#v", got)
	}

	defaultParams := imageGenerateCommandParams(imageGenerateCommandRequest{
		Path: "auto",
	}, "draw a robot")
	if _, ok := defaultParams["path"]; ok {
		t.Fatalf("did not expect default auto path to be passed, got %#v", defaultParams["path"])
	}
}

func imageCommandTestConfig(baseURL string) *config.Config {
	return &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				"OPENAI_IMAGE": {
					Enabled:         true,
					Protocol:        "openai",
					BaseURL:         baseURL,
					APIKey:          "test-key",
					DefaultModel:    "gpt-image-2",
					SupportedModels: []string{"gpt-image-2"},
					ModelCapabilities: map[string]config.ModelCapabilitySpec{
						"gpt-image-2": {
							NativeTools: config.NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
				"SENSENOVA_IMAGE": {
					Enabled:         true,
					Protocol:        "openai",
					BaseURL:         baseURL,
					APIKey:          "test-key",
					DefaultModel:    "sensenova-u1-fast",
					SupportedModels: []string{"sensenova-u1-fast"},
					ModelCapabilities: map[string]config.ModelCapabilitySpec{
						"sensenova-u1-fast": {
							NativeTools: config.NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}
}

func restoreGlobalConfig(t *testing.T) {
	t.Helper()
	previous := config.GetGlobalConfig()
	t.Cleanup(func() {
		config.SetGlobalConfig(previous)
	})
}

func assertJSONValue(t *testing.T, payload map[string]interface{}, key string, want interface{}) {
	t.Helper()
	if got := payload[key]; got != want {
		t.Fatalf("unexpected %s: %#v, want %#v", key, got, want)
	}
}
