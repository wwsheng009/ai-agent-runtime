package team

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PathClaimManager coordinates read/write path locks for team tasks.
type PathClaimManager struct {
	store Store
	root  string
}

// NewPathClaimManager creates a manager with an optional workspace root.
func NewPathClaimManager(store Store, workspaceRoot string) *PathClaimManager {
	return &PathClaimManager{store: store, root: workspaceRoot}
}

// Root returns the configured workspace root used for path normalization.
func (m *PathClaimManager) Root() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.root)
}

// CanClaim checks whether the requested paths can be claimed.
func (m *PathClaimManager) CanClaim(ctx context.Context, teamID string, readPaths, writePaths []string) (bool, []Conflict, error) {
	if m == nil || m.store == nil {
		return false, nil, fmt.Errorf("path claim manager is not initialized")
	}
	claims, err := m.store.ListPathClaims(ctx, teamID)
	if err != nil {
		return false, nil, err
	}
	now := time.Now().UTC()
	requestedReads := normalizePaths(m.root, readPaths)
	requestedWrites := normalizePaths(m.root, writePaths)
	conflicts := make([]Conflict, 0)
	for _, claim := range claims {
		if claim.LeaseUntil.Before(now) {
			continue
		}
		existingPath := normalizePath(m.root, claim.Path)
		if existingPath == "" {
			continue
		}
		for _, path := range requestedReads {
			if path == "" {
				continue
			}
			if !pathsOverlap(path, existingPath) {
				continue
			}
			if claim.Mode == PathClaimWrite {
				conflicts = append(conflicts, Conflict{
					Path:           path,
					ExistingPath:   existingPath,
					ExistingOwner:  claim.OwnerAgentID,
					ExistingTaskID: claim.TaskID,
					ExistingMode:   claim.Mode,
				})
			}
		}
		for _, path := range requestedWrites {
			if path == "" {
				continue
			}
			if !pathsOverlap(path, existingPath) {
				continue
			}
			conflicts = append(conflicts, Conflict{
				Path:           path,
				ExistingPath:   existingPath,
				ExistingOwner:  claim.OwnerAgentID,
				ExistingTaskID: claim.TaskID,
				ExistingMode:   claim.Mode,
			})
		}
	}
	if len(conflicts) > 0 {
		return false, conflicts, nil
	}
	return true, nil, nil
}

// Acquire registers path claims for a task.
func (m *PathClaimManager) Acquire(ctx context.Context, teamID, taskID, ownerAgentID string, readPaths, writePaths []string, leaseUntil time.Time) ([]PathClaim, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("path claim manager is not initialized")
	}
	if teamID == "" || taskID == "" || ownerAgentID == "" {
		return nil, fmt.Errorf("team id, task id, and owner are required")
	}
	if leaseUntil.IsZero() {
		leaseUntil = time.Now().UTC().Add(5 * time.Minute)
	}
	readPaths = normalizePaths(m.root, readPaths)
	writePaths = normalizePaths(m.root, writePaths)
	claims := make([]PathClaim, 0, len(readPaths)+len(writePaths))
	for _, path := range readPaths {
		if path == "" {
			continue
		}
		claims = append(claims, PathClaim{
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: ownerAgentID,
			Path:         path,
			Mode:         PathClaimRead,
			LeaseUntil:   leaseUntil,
		})
	}
	for _, path := range writePaths {
		if path == "" {
			continue
		}
		claims = append(claims, PathClaim{
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: ownerAgentID,
			Path:         path,
			Mode:         PathClaimWrite,
			LeaseUntil:   leaseUntil,
		})
	}
	if len(claims) == 0 {
		return nil, nil
	}
	if err := m.store.CreatePathClaims(ctx, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// Release removes all claims for the task.
func (m *PathClaimManager) Release(ctx context.Context, taskID string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("path claim manager is not initialized")
	}
	if taskID == "" {
		return nil
	}
	return m.store.ReleasePathClaimsByTask(ctx, taskID)
}

// Renew extends path claim leases for the task.
func (m *PathClaimManager) Renew(ctx context.Context, taskID string, leaseUntil time.Time) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("path claim manager is not initialized")
	}
	if taskID == "" {
		return nil
	}
	return m.store.RenewPathClaimsByTask(ctx, taskID, leaseUntil)
}

func normalizePaths(root string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized := normalizePath(root, path)
		if normalized == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func normalizePath(root, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	root = strings.TrimSpace(root)
	if root != "" && !filepath.IsAbs(clean) {
		rootClean := filepath.Clean(root)
		if rootClean != "" && rootClean != "." && !hasRootPrefix(clean, rootClean) {
			clean = filepath.Join(rootClean, clean)
		}
	}
	clean = filepath.Clean(clean)
	clean = filepath.ToSlash(clean)
	clean = strings.TrimSuffix(clean, "/")
	if clean == "." {
		return ""
	}
	if runtime.GOOS == "windows" {
		clean = strings.ToLower(clean)
	}
	return clean
}

func hasRootPrefix(path, root string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	root = filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if strings.HasPrefix(a, b+"/") {
		return true
	}
	if strings.HasPrefix(b, a+"/") {
		return true
	}
	return false
}
