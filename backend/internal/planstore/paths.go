package planstore

import (
	"fmt"
	"path/filepath"
	"strings"
)

// slug normalizes a path component so that it is safe to use as a directory or
// file name: only [a-z0-9-_] survive, everything else (CJK characters, spaces,
// path separators, punctuation) collapses into a single "-", and leading or
// trailing "-" are trimmed. An input that yields nothing returns "" so callers
// can apply their own fallback.
func slug(s string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
			pendingDash = false
			continue
		}
		if b.Len() > 0 && !pendingDash {
			b.WriteByte('-')
			pendingDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// slugOr returns slug(s) or fallback when the slug is empty.
func slugOr(s, fallback string) string {
	if out := slug(s); out != "" {
		return out
	}
	return fallback
}

// pathBase returns the final element of p, accepting both Windows ("\") and
// POSIX ("/") separators regardless of the host OS. filepath.Base alone is
// host-specific, so foreign separators are normalized first.
func pathBase(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// planNameFromPath derives the plan name from a plan file path, dropping a
// trailing ".md" extension (case-insensitive).
func planNameFromPath(planPath string) string {
	base := pathBase(planPath)
	if base == "" {
		return ""
	}
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".md") {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// IDFor derives the stable "<projectSlug>/<planName>" identifier used by the
// index and by the versions/ layout.
//
// Both paths may use either separator style; the result never contains path
// separators, so it can be compared and logged safely. Empty or fully
// non-ASCII inputs fall back to "project" / "plan".
func IDFor(projectPath, planPath string) string {
	return slugOr(pathBase(projectPath), "project") + "/" + slugOr(planNameFromPath(planPath), "plan")
}

// splitID splits a record id into its project and plan components, tolerating
// both separator styles. Only the last two components are meaningful, and they
// still need slugging before being used in a file path.
func splitID(id string) (project, plan string) {
	id = strings.TrimSpace(id)
	id = filepath.ToSlash(strings.ReplaceAll(id, `\`, "/"))
	id = strings.Trim(id, "/")
	if id == "" {
		return "", ""
	}
	parts := strings.Split(id, "/")
	plan = parts[len(parts)-1]
	if len(parts) > 1 {
		project = parts[len(parts)-2]
	}
	return project, plan
}

// projectSlugFromID returns the slugged project component of an id.
func projectSlugFromID(id string) string {
	project, _ := splitID(id)
	return slugOr(project, "project")
}

// versionRelPath is the root-relative, slash-separated snapshot location of one
// record version: versions/<projectSlug>/<planName>-v<N>.md.
//
// Both components are slugged again so that a hand-edited index can never make
// the store write outside its root directory.
func versionRelPath(id string, version int) string {
	project, plan := splitID(id)
	return fmt.Sprintf("versions/%s/%s-v%d.md", slugOr(project, "project"), slugOr(plan, "plan"), version)
}
