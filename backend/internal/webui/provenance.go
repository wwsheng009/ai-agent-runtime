package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

const frontendBuildInfoFile = "build-info.json"

// FrontendProvenance identifies the frontend assets embedded in the binary.
type FrontendProvenance struct {
	AssetManifestHash string `json:"asset_manifest_hash"`
	BuildTime         string `json:"build_time"`
	EntryAsset        string `json:"entry_asset"`
}

type manifestEntry struct {
	path        string
	contentHash string
}

var (
	provenanceOnce sync.Once
	provenance     FrontendProvenance
)

// Provenance returns metadata for the immutable embedded frontend.
func Provenance() FrontendProvenance {
	provenanceOnce.Do(func() {
		var err error
		provenance, err = provenanceFromFS(distFiles)
		if err != nil {
			provenance = unknownFrontendProvenance()
		}
	})
	return provenance
}

func provenanceFromFS(assets fs.FS) (FrontendProvenance, error) {
	declared, err := readDeclaredProvenance(assets)
	if err != nil {
		declared = FrontendProvenance{}
	}

	manifestHash, err := AssetManifestHash(assets)
	if err != nil {
		return FrontendProvenance{}, err
	}
	entryAsset, entryErr := entryAssetFromFS(assets)
	if entryErr != nil {
		entryAsset = declared.EntryAsset
	}

	return FrontendProvenance{
		AssetManifestHash: knownOrUnknown(manifestHash),
		BuildTime:         knownOrUnknown(declared.BuildTime),
		EntryAsset:        knownOrUnknown(normalizeEntryAsset(entryAsset)),
	}, nil
}

// AssetManifestHash calculates a deterministic SHA-256 over all embedded
// files except build-info.json. Each sorted record is "content-hash  path\n".
func AssetManifestHash(assets fs.FS) (string, error) {
	entries := make([]manifestEntry, 0)
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || name == frontendBuildInfoFile {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		entries = append(entries, manifestEntry{
			path:        name,
			contentHash: hex.EncodeToString(digest[:]),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk frontend assets: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	manifest := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(manifest, "%s  %s\n", entry.contentHash, entry.path)
	}
	return hex.EncodeToString(manifest.Sum(nil)), nil
}

func readDeclaredProvenance(assets fs.FS) (FrontendProvenance, error) {
	contents, err := fs.ReadFile(assets, frontendBuildInfoFile)
	if errors.Is(err, fs.ErrNotExist) {
		return FrontendProvenance{}, nil
	}
	if err != nil {
		return FrontendProvenance{}, fmt.Errorf("read frontend build info: %w", err)
	}
	var result FrontendProvenance
	if err := json.Unmarshal(contents, &result); err != nil {
		return FrontendProvenance{}, fmt.Errorf("decode frontend build info: %w", err)
	}
	return result, nil
}

func entryAssetFromFS(assets fs.FS) (string, error) {
	contents, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return "", fmt.Errorf("read frontend index: %w", err)
	}
	document, err := html.Parse(bytes.NewReader(contents))
	if err != nil {
		return "", fmt.Errorf("parse frontend index: %w", err)
	}

	var findEntry func(*html.Node) string
	findEntry = func(node *html.Node) string {
		if node.Type == html.ElementNode && node.Data == "script" {
			var scriptType, source string
			for _, attribute := range node.Attr {
				switch strings.ToLower(attribute.Key) {
				case "type":
					scriptType = strings.TrimSpace(attribute.Val)
				case "src":
					source = strings.TrimSpace(attribute.Val)
				}
			}
			if strings.EqualFold(scriptType, "module") && source != "" {
				return source
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if source := findEntry(child); source != "" {
				return source
			}
		}
		return ""
	}
	if entry := findEntry(document); entry != "" {
		return normalizeEntryAsset(entry), nil
	}
	return "", errors.New("frontend module entry asset not found")
}

func normalizeEntryAsset(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "://") {
		return value
	}
	return "/" + strings.TrimPrefix(value, "./")
}

func knownOrUnknown(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

func unknownFrontendProvenance() FrontendProvenance {
	return FrontendProvenance{
		AssetManifestHash: "unknown",
		BuildTime:         "unknown",
		EntryAsset:        "unknown",
	}
}
