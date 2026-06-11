package config

import "path/filepath"

// Layout derives the on-disk paths used by the pipeline from the configured
// roots. Everything writable lives under WorkRoot (the work volume); only
// published output lands under OutputRoot.
type Layout struct {
	WorkRoot   string
	OutputRoot string
}

// NewLayout builds a Layout from the environment config.
func NewLayout(e *Env) Layout {
	return Layout{WorkRoot: e.WorkRoot, OutputRoot: e.OutputRoot}
}

// RepoDir is the persistent shallow clone for a site.
func (l Layout) RepoDir(slug string) string { return filepath.Join(l.WorkRoot, "repos", slug) }

// BuildsDir holds per-build output directories for a site, pruned by retention.
func (l Layout) BuildsDir(slug string) string { return filepath.Join(l.WorkRoot, "builds", slug) }

// BuildDir is the destination for a single hugo run.
func (l Layout) BuildDir(slug, buildID string) string {
	return filepath.Join(l.BuildsDir(slug), buildID)
}

// CacheDir is the per-site Hugo cache (passed via --cacheDir).
func (l Layout) CacheDir(slug string) string { return filepath.Join(l.WorkRoot, "cache", slug) }

// StateDir holds per-site state JSON files.
func (l Layout) StateDir() string { return filepath.Join(l.WorkRoot, "state") }

// HomeDir is a writable HOME for git/hugo under a read-only rootfs.
func (l Layout) HomeDir() string { return filepath.Join(l.WorkRoot, "home") }

// LockPath is the single-instance flock target.
func (l Layout) LockPath() string { return filepath.Join(l.WorkRoot, ".lock") }

// LiveDir is the published site root that Caddy serves.
func (l Layout) LiveDir(publishDir string) string { return filepath.Join(l.OutputRoot, publishDir) }
