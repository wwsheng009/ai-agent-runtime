package toolcore

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	artifactIDPattern     = regexp.MustCompile(`^art_[0-9a-f]{32}$`)
	artifactTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// ArtifactRef names an artifact within exactly one store.
//
// Local artifacts retain the historical bare `art_...` spelling. Remote
// artifacts are always `remote:<target>:art_...`; a bare id is resolved by the
// caller only when the originating scope is known.
type ArtifactRef struct {
	Scope  ArtifactScope
	Target string
	ID     string
}

// String returns the canonical wire form.
func (r ArtifactRef) String() string {
	switch r.Scope {
	case ArtifactScopeRemote:
		if r.Target == "" {
			return r.ID
		}
		return "remote:" + r.Target + ":" + r.ID
	default:
		return r.ID
	}
}

// IsZero reports whether the ref carries no id.
func (r ArtifactRef) IsZero() bool {
	return strings.TrimSpace(r.ID) == ""
}

// Validate checks scope/target/id invariants and rejects path-like input.
func (r ArtifactRef) Validate() error {
	id := strings.TrimSpace(r.ID)
	if !artifactIDPattern.MatchString(id) {
		return fmt.Errorf("%s: invalid artifact id %q", ErrArtifactScopeDenied, id)
	}
	switch r.Scope {
	case ArtifactScopeLocal, "":
		if strings.TrimSpace(r.Target) != "" {
			return fmt.Errorf("%s: local artifact must not carry a target", ErrArtifactScopeDenied)
		}
		return nil
	case ArtifactScopeRemote:
		target := strings.TrimSpace(r.Target)
		if !artifactTargetPattern.MatchString(target) {
			return fmt.Errorf("%s: invalid artifact target %q", ErrArtifactScopeDenied, target)
		}
		return nil
	default:
		return fmt.Errorf("%s: unknown artifact scope %q", ErrArtifactScopeDenied, r.Scope)
	}
}

// ParseArtifactRef accepts either a canonical local/remote ref or a bare id.
// Bare ids are returned as local refs; callers must apply Origin-aware
// ownership before using one.
func ParseArtifactRef(raw string) (ArtifactRef, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ArtifactRef{}, fmt.Errorf("%s: artifact ref is empty", ErrArtifactScopeDenied)
	}
	if !strings.HasPrefix(value, "remote:") {
		ref := ArtifactRef{Scope: ArtifactScopeLocal, ID: value}
		if err := ref.Validate(); err != nil {
			return ArtifactRef{}, err
		}
		return ref, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return ArtifactRef{}, fmt.Errorf("%s: malformed remote artifact ref %q", ErrArtifactScopeDenied, value)
	}
	ref := ArtifactRef{Scope: ArtifactScopeRemote, Target: parts[1], ID: parts[2]}
	if err := ref.Validate(); err != nil {
		return ArtifactRef{}, err
	}
	return ref, nil
}

// ResolveBareArtifactRef assigns a bare id to the origin's store. Local origins
// stay local; remote origins are scoped to the configured target. An explicit
// remote ref for another target is rejected.
func ResolveBareArtifactRef(raw string, origin ResultOrigin) (ArtifactRef, error) {
	ref, err := ParseArtifactRef(raw)
	if err != nil {
		return ArtifactRef{}, err
	}
	if ref.Scope == ArtifactScopeRemote {
		want := strings.TrimSpace(origin.Target)
		if !origin.IsRemote() || want == "" || ref.Target != want {
			return ArtifactRef{}, fmt.Errorf("%s: artifact target %q is not the active target", ErrArtifactScopeDenied, ref.Target)
		}
		return ref, nil
	}
	if origin.IsRemote() {
		if strings.TrimSpace(origin.Target) == "" {
			return ArtifactRef{}, fmt.Errorf("%s: remote origin has no target", ErrArtifactScopeDenied)
		}
		ref.Scope = ArtifactScopeRemote
		ref.Target = origin.Target
	}
	if err := ref.Validate(); err != nil {
		return ArtifactRef{}, err
	}
	return ref, nil
}

// ValidArtifactID reports whether id is a generated local id.
func ValidArtifactID(id string) bool {
	return artifactIDPattern.MatchString(strings.TrimSpace(id))
}

// ValidArtifactTarget reports whether target is safe for a storage namespace.
func ValidArtifactTarget(target string) bool {
	return artifactTargetPattern.MatchString(strings.TrimSpace(target))
}
