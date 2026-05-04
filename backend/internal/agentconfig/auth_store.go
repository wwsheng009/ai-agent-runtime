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

const (
	AuthKeyTypeAPIKey = "api_key"
	AuthKeyTypeOAuth  = "oauth"
)

// ProviderAuthRecord stores user-level credentials that must not be written to config.yaml.
type ProviderAuthRecord struct {
	KeyType      string `json:"key_type,omitempty"`
	AuthMode     string `json:"auth_mode,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
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

func normalizeProviderAuthKeyType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", AuthKeyTypeAPIKey, AuthKeyTypeOAuth:
		return strings.ToLower(strings.TrimSpace(value))
	case "apikey", "api-key", "key":
		return AuthKeyTypeAPIKey
	case "access_token", "oauth_token", "oauth-access-token":
		return AuthKeyTypeOAuth
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func inferProviderAuthKeyType(record ProviderAuthRecord) string {
	if keyType := normalizeProviderAuthKeyType(record.KeyType); keyType != "" {
		return keyType
	}
	switch strings.ToLower(strings.TrimSpace(record.AuthMode)) {
	case AuthKeyTypeAPIKey, "apikey", "api-key", "key":
		return AuthKeyTypeAPIKey
	case AuthKeyTypeOAuth, "chatgpt", "device-code", "device_code":
		return AuthKeyTypeOAuth
	}
	if strings.TrimSpace(record.APIKey) != "" {
		return AuthKeyTypeAPIKey
	}
	if strings.TrimSpace(record.AccessToken) != "" || strings.TrimSpace(record.RefreshToken) != "" || strings.TrimSpace(record.IDToken) != "" {
		return AuthKeyTypeOAuth
	}
	return ""
}

func normalizeProviderAuthRecord(record ProviderAuthRecord) ProviderAuthRecord {
	record.KeyType = normalizeProviderAuthKeyType(record.KeyType)
	record.AuthMode = strings.ToLower(strings.TrimSpace(record.AuthMode))
	if record.KeyType == "" {
		record.KeyType = inferProviderAuthKeyType(record)
	}
	if record.AuthMode == "" {
		record.AuthMode = record.KeyType
	}
	switch record.KeyType {
	case AuthKeyTypeAPIKey:
		record.APIKey = strings.TrimSpace(record.APIKey)
		record.Issuer = ""
		record.ClientID = ""
		record.IDToken = ""
		record.AccessToken = ""
		record.RefreshToken = ""
	case AuthKeyTypeOAuth:
		record.Issuer = strings.TrimSpace(record.Issuer)
		record.ClientID = strings.TrimSpace(record.ClientID)
		record.IDToken = strings.TrimSpace(record.IDToken)
		record.AccessToken = strings.TrimSpace(record.AccessToken)
		record.RefreshToken = strings.TrimSpace(record.RefreshToken)
		record.APIKey = ""
	default:
		record.APIKey = strings.TrimSpace(record.APIKey)
		record.Issuer = strings.TrimSpace(record.Issuer)
		record.ClientID = strings.TrimSpace(record.ClientID)
		record.IDToken = strings.TrimSpace(record.IDToken)
		record.AccessToken = strings.TrimSpace(record.AccessToken)
		record.RefreshToken = strings.TrimSpace(record.RefreshToken)
	}
	if strings.TrimSpace(record.UpdatedAt) == "" {
		record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		record.UpdatedAt = strings.TrimSpace(record.UpdatedAt)
	}
	return record
}

func expandProviderAuthRecordEnvVars(record ProviderAuthRecord) ProviderAuthRecord {
	record.APIKey = strings.TrimSpace(expandEnvVars(record.APIKey))
	record.IDToken = strings.TrimSpace(expandEnvVars(record.IDToken))
	record.AccessToken = strings.TrimSpace(expandEnvVars(record.AccessToken))
	record.RefreshToken = strings.TrimSpace(expandEnvVars(record.RefreshToken))
	return record
}

func providerAuthSecretForKeyType(record ProviderAuthRecord, keyType string) string {
	switch normalizeProviderAuthKeyType(keyType) {
	case AuthKeyTypeAPIKey:
		return strings.TrimSpace(record.APIKey)
	case AuthKeyTypeOAuth:
		return strings.TrimSpace(record.AccessToken)
	default:
		if secret := strings.TrimSpace(record.APIKey); secret != "" {
			return secret
		}
		return strings.TrimSpace(record.AccessToken)
	}
}

func LoadProviderAuthSecret(ref, keyType string) (string, error) {
	return LoadProviderAuthSecretFromPath(DefaultAuthStorePath(), ref, keyType)
}

func LoadProviderAuthSecretFromPath(path, ref, keyType string) (string, error) {
	record, err := LoadProviderAuthFromPath(path, ref)
	if err != nil {
		return "", err
	}
	secret := providerAuthSecretForKeyType(*record, keyType)
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("auth ref %q has no secret for key type %q", ref, normalizeProviderAuthKeyType(keyType))
	}
	return secret, nil
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
	normalized := expandProviderAuthRecordEnvVars(normalizeProviderAuthRecord(record))
	return &normalized, nil
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
	record = normalizeProviderAuthRecord(record)
	switch record.KeyType {
	case AuthKeyTypeAPIKey:
		if strings.TrimSpace(record.APIKey) == "" {
			return fmt.Errorf("api key is required for auth ref %q", ref)
		}
	case AuthKeyTypeOAuth:
		if strings.TrimSpace(record.AccessToken) == "" {
			return fmt.Errorf("access token is required for auth ref %q", ref)
		}
	default:
		return fmt.Errorf("key type is required for auth ref %q", ref)
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
	for ref, record := range document.Providers {
		document.Providers[ref] = normalizeProviderAuthRecord(record)
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
