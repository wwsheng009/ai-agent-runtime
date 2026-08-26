//go:build !go1.20 || go1.21

package toolschema

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
)

const schemaBaseURI = "urn:aicli:tool-schema"
const draft7SchemaURI = "https://json-schema.org/draft-07/schema#"

// Validate compiles a canonical tool schema. External references are rejected
// without touching the filesystem or network.
func Validate(schema map[string]interface{}) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal tool schema for validation: %w", err)
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse tool schema: %w", err)
	}
	// MCP schemas commonly omit $schema and historically use draft-07 tuple
	// semantics. Explicit declarations still take precedence.
	if parsed.Schema == "" {
		parsed.Schema = draft7SchemaURI
	}
	_, err = parsed.Resolve(&jsonschema.ResolveOptions{
		BaseURI: schemaBaseURI,
		Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			return nil, fmt.Errorf("external schema references are disabled: %s", uri.String())
		},
	})
	if err != nil {
		return fmt.Errorf("compile tool schema: %w", err)
	}
	return nil
}

// CanonicalizeAndValidate prepares an immutable schema snapshot and verifies
// that providers can safely consume it.
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
