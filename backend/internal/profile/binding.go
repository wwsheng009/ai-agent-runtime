package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalidProjectProfileBindingWorkspace indicates that the workspace passed
// to LoadProjectProfileBinding is not a usable directory.  Binding document
// errors deliberately do not use this error: they are returned in the
// ProjectProfileBinding metadata so discovery can report the rest of the
// profile list without silently falling back to another layer.
var ErrInvalidProjectProfileBindingWorkspace = errors.New("invalid project profile binding workspace")

// ProjectProfileBinding is the read-only result of inspecting
// <workspace>/.aicli/profile.  It is intentionally only a pointer and target
// description; loading this value never changes defaults, session metadata, or
// an actor.
type ProjectProfileBinding struct {
	Present                 bool   `json:"present"`
	Valid                   bool   `json:"valid"`
	Workspace               string `json:"workspace_path,omitempty"`
	Path                    string `json:"path,omitempty"`
	Ref                     string `json:"ref,omitempty"`
	Root                    string `json:"profile_root,omitempty"`
	Layer                   string `json:"layer,omitempty"`
	Source                  string `json:"source,omitempty"`
	Error                   string `json:"error,omitempty"`
	PromptSuppressed        bool   `json:"prompt_suppressed,omitempty"`
	PromptSuppressionReason string `json:"prompt_suppression_reason,omitempty"`
}

type projectProfileBindingDocument struct {
	Profile string `yaml:"profile"`
}

// LoadProjectProfileBinding discovers the project profile pointer for a real
// workspace.  The only accepted pointer file is <workspace>/.aicli/profile and
// the only accepted target is <workspace>/.aicli/profiles/<ref>/profile.yaml.
//
// A missing pointer file is not an error and returns Present=false.  Once the
// pointer file exists, malformed content and a missing target are represented
// by Present=true, Valid=false and Error populated.  This distinction is
// important to callers: a bad project binding must not be mistaken for an
// absent binding and resolved from user/default layers instead.
func LoadProjectProfileBinding(workspace string) (*ProjectProfileBinding, error) {
	cleanWorkspace, err := normalizeProjectBindingWorkspace(workspace)
	if err != nil {
		return nil, err
	}

	bindingPath := filepath.Join(cleanWorkspace, ".aicli", "profile")
	binding := &ProjectProfileBinding{
		Workspace: cleanWorkspace,
		Path:      bindingPath,
		Layer:     "project",
		Source:    "project_binding",
	}

	data, err := os.ReadFile(bindingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return binding, nil
		}
		binding.Present = true
		binding.Error = fmt.Sprintf("读取 project profile binding 失败：%v", err)
		return binding, nil
	}
	binding.Present = true

	ref, err := parseProjectProfileBinding(data)
	if err != nil {
		binding.Error = err.Error()
		return binding, nil
	}
	binding.Ref = ref

	projectRoot := filepath.Join(cleanWorkspace, ".aicli", "profiles")
	targetRoot := filepath.Join(projectRoot, ref)
	if !projectProfileBindingPathWithin(projectRoot, targetRoot) {
		// Keep this check even though the ref validator rejects separators.  It
		// makes the containment invariant explicit at the final filesystem
		// boundary and prevents a future validator relaxation from becoming a
		// traversal bug.
		binding.Error = fmt.Sprintf("project profile binding target escapes project layer: %q", ref)
		return binding, nil
	}
	binding.Root = targetRoot
	profileFile := filepath.Join(targetRoot, "profile.yaml")
	info, statErr := os.Stat(profileFile)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			binding.Error = fmt.Sprintf("project profile target 不存在：%s", profileFile)
		} else {
			binding.Error = fmt.Sprintf("检查 project profile target 失败：%v", statErr)
		}
		return binding, nil
	}
	if info.IsDir() {
		binding.Error = fmt.Sprintf("project profile target 不是文件：%s", profileFile)
		return binding, nil
	}

	// Valid describes a valid pointer and an existing project target.  The
	// target profile.yaml itself is intentionally not parsed here; list
	// discovery separately reports profile schema/YAML validity and this helper
	// must not create a second profile resolver.
	binding.Valid = true
	return binding, nil
}

func normalizeProjectBindingWorkspace(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("%w: workspace is required", ErrInvalidProjectProfileBindingWorkspace)
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("%w: resolve workspace %q: %v", ErrInvalidProjectProfileBindingWorkspace, workspace, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: workspace %q: %v", ErrInvalidProjectProfileBindingWorkspace, abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: workspace is not a directory: %s", ErrInvalidProjectProfileBindingWorkspace, abs)
	}
	return abs, nil
}

// parseProjectProfileBinding performs both structural and semantic validation
// of the tiny pointer document.  yaml.Decoder.KnownFields handles unknown
// struct fields; the yaml.Node walk additionally rejects duplicate keys and
// object/sequence values before the value can be interpreted as a ref.
func parseProjectProfileBinding(data []byte) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("project profile binding 不能为空")
		}
		return "", fmt.Errorf("解析 project profile binding YAML 失败：%w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0] == nil {
		return "", fmt.Errorf("project profile binding 必须是单一 YAML 文档")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("project profile binding 顶层必须是 mapping")
	}

	seen := make(map[string]struct{}, len(root.Content)/2)
	for index := 0; index < len(root.Content); index += 2 {
		if index+1 >= len(root.Content) {
			return "", fmt.Errorf("project profile binding mapping 无效")
		}
		key := root.Content[index]
		value := root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Value == "" {
			return "", fmt.Errorf("project profile binding 字段名必须是非空标量")
		}
		if _, exists := seen[key.Value]; exists {
			return "", fmt.Errorf("project profile binding 包含重复字段：%s", key.Value)
		}
		seen[key.Value] = struct{}{}
		if key.Value != "profile" {
			return "", fmt.Errorf("project profile binding 包含未知字段：%s", key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return "", fmt.Errorf("project profile binding.profile 必须是字符串标量")
		}
	}
	if _, ok := seen["profile"]; !ok {
		return "", fmt.Errorf("project profile binding 缺少 profile 字段")
	}

	// Decode a second time through the strict struct decoder.  Keeping this
	// explicit makes the KnownFields contract durable if the document type is
	// extended later, while the AST pass above retains duplicate-key coverage.
	strictDecoder := yaml.NewDecoder(bytes.NewReader(data))
	strictDecoder.KnownFields(true)
	var parsed projectProfileBindingDocument
	if err := strictDecoder.Decode(&parsed); err != nil {
		return "", fmt.Errorf("解析 project profile binding 字段失败：%w", err)
	}
	var trailing yaml.Node
	if err := strictDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("project profile binding 不允许多个 YAML 文档")
		}
		return "", fmt.Errorf("读取 project profile binding 尾部失败：%w", err)
	}

	ref := strings.TrimSpace(parsed.Profile)
	if err := validateProjectProfileBindingRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

func validateProjectProfileBindingRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("project profile binding.profile 不能为空")
	}
	if ref == "." || ref == ".." {
		return fmt.Errorf("project profile binding.profile 不允许是路径段：%q", ref)
	}
	// Check all path forms before filepath.Join.  The explicit slash checks
	// keep the contract clear even on a non-Windows host running cross-platform
	// tests; VolumeName/IsAbs cover native drive and UNC forms on Windows.
	if filepath.IsAbs(ref) || filepath.VolumeName(ref) != "" ||
		strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) ||
		strings.HasPrefix(ref, `//`) || strings.HasPrefix(ref, `\\`) {
		return fmt.Errorf("project profile binding.profile 不允许绝对路径：%q", ref)
	}
	if strings.ContainsAny(ref, `/\\:`) {
		return fmt.Errorf("project profile binding.profile 只能是单一 profile 名：%q", ref)
	}
	if cleaned := filepath.Clean(ref); cleaned != ref || strings.Contains(ref, "..") {
		return fmt.Errorf("project profile binding.profile 不允许路径穿越：%q", ref)
	}
	if err := ValidateProfileName(ref); err != nil {
		return fmt.Errorf("project profile binding.profile 非法：%w", err)
	}
	return nil
}

func projectProfileBindingPathWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
