// Package build runs the version-selected Hugo binary against a site
// checkout and validates the produced output before it may be published.
package build

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const outputTailBytes = 4096

// Runner executes hugo builds.
type Runner struct {
	HomeDir string // writable HOME under the work volume (read-only rootfs)
}

// Input describes one hugo invocation.
type Input struct {
	Binary      string // resolved hugo binary path
	SourceDir   string // git checkout
	DestDir     string // fresh, empty build output dir
	CacheDir    string // per-site cache (--cacheDir), on the work volume
	Environment string // --environment
	BaseURL     string // optional --baseURL
	Timeout     time.Duration
}

// Run executes hugo with a fixed flag set (custom build commands are
// unsupported by design). On failure the error carries the tail of hugo's
// combined output so broken sites are debuggable from logs.
func (r *Runner) Run(ctx context.Context, in Input) error {
	for _, dir := range []string{in.DestDir, in.CacheDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	args := []string{
		"--source", in.SourceDir,
		"--destination", in.DestDir,
		"--cacheDir", in.CacheDir,
		"--environment", in.Environment,
	}
	if in.BaseURL != "" {
		args = append(args, "--baseURL", in.BaseURL)
	}

	buildCtx := ctx
	if in.Timeout > 0 {
		var cancel context.CancelFunc
		buildCtx, cancel = context.WithTimeout(ctx, in.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(buildCtx, in.Binary, args...)
	cmd.Dir = in.SourceDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.HomeDir,
		"XDG_CACHE_HOME=" + in.CacheDir,
		"HUGO_ENVIRONMENT=" + in.Environment,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > outputTailBytes {
			tail = tail[len(tail)-outputTailBytes:]
		}
		if buildCtx.Err() != nil {
			err = fmt.Errorf("%w (%v)", buildCtx.Err(), err)
		}
		return fmt.Errorf("hugo build failed: %w: %s", err, tail)
	}
	return nil
}

// ValidateOutput refuses an empty build so a broken build can never wipe a
// live site. The directory must contain at least one regular file.
func ValidateOutput(dir string) error {
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate build output %s: %w", dir, err)
	}
	if !found {
		return fmt.Errorf("build output %s contains no files; refusing to publish an empty build", dir)
	}
	return nil
}
