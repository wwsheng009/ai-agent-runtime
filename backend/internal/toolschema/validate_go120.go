//go:build go1.20 && !go1.21

package toolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// 常量与 validate.go 中的定义保持一致（两文件按 build tag 互斥编译，
// 因此可以各自声明）。
const schemaBaseURI = "urn:aicli:tool-schema"
const draft7SchemaURI = "https://json-schema.org/draft-07/schema#"

// Validate compiles a canonical tool schema using santhosh-tekuri/jsonschema
// v5, the only maintained JSON Schema compiler compatible with the Go 1.20
// toolchain used for Windows 7 builds. External references are rejected without
// touching the filesystem or network, mirroring the jsonschema-go
// implementation in validate.go (which requires go >= 1.23).
func Validate(schema map[string]interface{}) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal tool schema for validation: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7
	compiler.LoadURL = func(url string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are disabled: %s", url)
	}
	if err := compiler.AddResource(schemaBaseURI, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("parse tool schema: %w", err)
	}
	if _, err := compiler.Compile(schemaBaseURI); err != nil {
		return fmt.Errorf("compile tool schema: %w", err)
	}
	return nil
}

// CanonicalizeAndValidate prepares an immutable schema snapshot and verifies
// that providers can safely consume it. 与 validate.go 中的实现一致
// （Canonicalize 来自 canonical.go）。
func CanonicalizeAndValidate(schema map[string]interface{}) (map[string]interface{}, []Warning, error) {
	canonical, warnings, err := Canonicalize(schema)
	if err != nil {
		return nil, warnings, err
	}
	if err := Validate(canonical); err != nil {
		return nil, warnings, err
	}
	return canonical, warnings, nil
}
