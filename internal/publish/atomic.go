// Package publish moves validated build output into the live directory that
// Caddy serves, atomically. The staging directory always lives on the
// destination filesystem, so the final swap into the live name is a same-FS
// rename even when the work and output roots are on different mounts —
// readers never see a half-copied site.
package publish

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	stagingInfix = ".tmp-"
	prevSuffix   = ".__prev"
)

// Publisher performs atomic publishes. rename is injectable for tests
// (e.g. to simulate EXDEV); when nil, os.Rename is used.
type Publisher struct {
	rename func(oldpath, newpath string) error
}

// New returns a Publisher using os.Rename.
func New() *Publisher { return &Publisher{rename: os.Rename} }

// NewWithRename returns a Publisher with a custom rename, for tests.
func NewWithRename(rename func(string, string) error) *Publisher {
	return &Publisher{rename: rename}
}

// Publish replaces liveDir with the contents of buildDir.
//
//  1. Stage: rename buildDir to <liveDir>.tmp-<buildID>; on EXDEV (work and
//     output on different filesystems) fall back to copying into the staging
//     path. Either way staging ends up on the destination filesystem.
//  2. Swap: move any existing liveDir aside to <liveDir>.__prev, rename
//     staging into the live name, then delete the previous copy.
//
// If the swap fails the previous live directory is restored, so a failed
// publish never takes a site down.
//
// Publish is safe to retry with the same arguments: a failed swap keeps the
// staging directory (buildDir has already been renamed away) and the retry
// reuses it. Stale staging or previous dirs left by older builds of the same
// site are cleared first, so one wedged cleanup can't block publishes forever.
func (p *Publisher) Publish(buildDir, liveDir, buildID string) error {
	if err := os.MkdirAll(filepath.Dir(liveDir), 0o755); err != nil {
		return fmt.Errorf("create output root: %w", err)
	}
	staging := liveDir + stagingInfix + buildID
	if err := removeStaleSiblings(liveDir, staging); err != nil {
		return err
	}

	if _, err := os.Stat(staging); err != nil { // not already staged by a failed attempt
		if err := p.rename(buildDir, staging); err != nil {
			if !errors.Is(err, syscall.EXDEV) {
				return fmt.Errorf("stage build: %w", err)
			}
			if err := copyTree(buildDir, staging); err != nil {
				os.RemoveAll(staging) // a partial copy is unusable; retry re-copies from buildDir
				return fmt.Errorf("stage build (cross-device copy): %w", err)
			}
		}
	}

	prev := liveDir + prevSuffix
	hadLive := true
	if err := p.rename(liveDir, prev); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("move live dir aside: %w", err)
		}
		hadLive = false
	}
	if err := p.rename(staging, liveDir); err != nil {
		if hadLive {
			if rbErr := p.rename(prev, liveDir); rbErr != nil {
				return fmt.Errorf("activate new build: %w (ROLLBACK ALSO FAILED: %v)", err, rbErr)
			}
		}
		return fmt.Errorf("activate new build: %w (previous version restored)", err)
	}
	if hadLive {
		if err := os.RemoveAll(prev); err != nil {
			return fmt.Errorf("published, but failed to remove previous version %s: %w", prev, err)
		}
	}
	return nil
}

// removeStaleSiblings clears leftovers from earlier publishes of this site:
// any <liveDir>.tmp-* other than the current staging path, and a lingering
// <liveDir>.__prev whose removal failed after a completed swap (which would
// otherwise make every future move-aside rename fail with ENOTEMPTY).
func removeStaleSiblings(liveDir, staging string) error {
	dir := filepath.Dir(liveDir)
	base := filepath.Base(liveDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read output root: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != base+prevSuffix && !strings.HasPrefix(name, base+stagingInfix) {
			continue
		}
		full := filepath.Join(dir, name)
		if full == staging {
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("remove stale publish dir %s: %w", full, err)
		}
	}
	return nil
}

// SweepOrphans removes stale staging and previous-version directories left
// in outputRoot by a crash mid-publish. Call at startup before initial sync.
func SweepOrphans(outputRoot string) ([]string, error) {
	entries, err := os.ReadDir(outputRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read output root: %w", err)
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, stagingInfix) || strings.HasSuffix(name, prevSuffix) {
			full := filepath.Join(outputRoot, name)
			if err := os.RemoveAll(full); err != nil {
				return removed, fmt.Errorf("remove orphan %s: %w", full, err)
			}
			removed = append(removed, full)
		}
	}
	return removed, nil
}

// PruneBuilds keeps the newest `keep` build directories under buildsDir
// (ordered by modification time) and removes the rest.
func PruneBuilds(buildsDir string, keep int) error {
	entries, err := os.ReadDir(buildsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read builds dir: %w", err)
	}
	type dirInfo struct {
		name string
		mod  int64
	}
	var dirs []dirInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{e.Name(), info.ModTime().UnixNano()})
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod > dirs[j].mod }) // newest first
	for _, d := range dirs[keep:] {
		if err := os.RemoveAll(filepath.Join(buildsDir, d.name)); err != nil {
			return fmt.Errorf("prune build %s: %w", d.name, err)
		}
	}
	return nil
}

// copyTree copies a directory tree (dirs, regular files, symlinks),
// preserving file modes. Hugo output is world-readable by default, which the
// separate Caddy container relies on.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(path, target, d)
		default:
			return fmt.Errorf("unsupported file type %s at %s", d.Type(), path)
		}
	})
}

func copyFile(src, dst string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
