package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const authStoreVersion = 1

// ProviderAuthRecord stores user-level credentials that must not be written to config.yaml.
type ProviderAuthRecord struct {
	AuthMode     string `json:"auth_mode"`
	Issuer       string `json:"issuer,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type providerAuthStoreDocument struct {
	Version   int                           `json:"version"`
	Providers map[string]ProviderAuthRecord `json:"providers"`
}

// DefaultAuthStorePath returns the user-level auth store path.
func DefaultAuthStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", "auth.json")
}

// LoadProviderAuth loads one auth record by reference from the default auth store.
func LoadProviderAuth(ref string) (*ProviderAuthRecord, error) {
	return LoadProviderAuthFromPath(DefaultAuthStorePath(), ref)
}

// LoadProviderAuthFromPath loads one auth record by reference.
func LoadProviderAuthFromPath(path, ref string) (*ProviderAuthRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("auth ref is required")
	}
	document, err := readProviderAuthStore(path)
	if err != nil {
		return nil, err
	}
	record, ok := document.Providers[ref]
	if !ok {
		return nil, fmt.Errorf("auth ref %q not found", ref)
	}
	return &record, nil
}

// SaveProviderAuth stores one auth record in the default user-level auth store.
func SaveProviderAuth(ref string, record ProviderAuthRecord) error {
	return SaveProviderAuthToPath(DefaultAuthStorePath(), ref, record)
}

// SaveProviderAuthToPath stores one auth record by reference.
func SaveProviderAuthToPath(path, ref string, record ProviderAuthRecord) error {
	path = strings.TrimSpace(path)
	ref = strings.TrimSpace(ref)
	if path == "" {
		return fmt.Errorf("auth store path is required")
	}
	if ref == "" {
		return fmt.Errorf("auth ref is required")
	}
	document, err := readProviderAuthStore(path)
	if err != nil {
		return err
	}
	if document.Providers == nil {
		document.Providers = make(map[string]ProviderAuthRecord)
	}
	record.AuthMode = strings.TrimSpace(record.AuthMode)
	if record.AuthMode == "" {
		record.AuthMode = "oauth"
	}
	if strings.TrimSpace(record.UpdatedAt) == "" {
		record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	document.Providers[ref] = record
	return writeProviderAuthStore(path, document)
}

func readProviderAuthStore(path string) (*providerAuthStoreDocument, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("auth store path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &providerAuthStoreDocument{
				Version:   authStoreVersion,
				Providers: make(map[string]ProviderAuthRecord),
			}, nil
		}
		return nil, fmt.Errorf("read auth store %s: %w", path, err)
	}
	document := &providerAuthStoreDocument{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, document); err != nil {
			return nil, fmt.Errorf("parse auth store %s: %w", path, err)
		}
	}
	if document.Version == 0 {
		document.Version = authStoreVersion
	}
	if document.Providers == nil {
		document.Providers = make(map[string]ProviderAuthRecord)
	}
	return document, nil
}

func writeProviderAuthStore(path string, document *providerAuthStoreDocument) error {
	if document == nil {
		return fmt.Errorf("auth store document is nil")
	}
	document.Version = authStoreVersion
	if document.Providers == nil {
		document.Providers = make(map[string]ProviderAuthRecord)
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth store: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create auth store directory: %w", err)
		}
	}

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp auth store: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("prepare auth store permissions: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		return fmt.Errorf("write temp auth store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp auth store: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return fmt.Errorf("replace auth store: %w (retry after remove: %v)", err, retryErr)
		}
	}
	return nil
}
