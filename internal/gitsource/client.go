// Package gitsource syncs site repositories over HTTPS by shelling out to
// the system git binary. Tokens are injected via GIT_CONFIG_* environment
// variables so they never appear in argv, URLs, or on-disk config, and are
// redacted from all error output.
package gitsource

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
)

const outputTailBytes = 4096

// Client runs git operations for site repositories.
type Client struct {
	GitBin  string        // path to git; "git" resolves via PATH
	Timeout time.Duration // per git operation
	HomeDir string        // writable HOME under the work volume
}

// New returns a Client with the given per-operation timeout and HOME.
func New(timeout time.Duration, homeDir string) *Client {
	return &Client{GitBin: "git", Timeout: timeout, HomeDir: homeDir}
}

// Sync brings repoDir to the tip of the site's branch (shallow) and
// initialises submodules. It returns the checked-out commit hash. The same
// flow handles both first clone and subsequent updates: init + remote
// set-url + fetch --depth=1 + reset --hard FETCH_HEAD, so branch changes in
// sites.yaml never fight a pinned single-branch clone.
func (c *Client) Sync(ctx context.Context, site *config.Site, repoDir string) (string, error) {
	env, err := c.gitEnv(site)
	if err != nil {
		return "", err
	}
	redact := newRedactor(site.Token)

	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return "", fmt.Errorf("create repo dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		if _, err := c.run(ctx, repoDir, env, redact, "init", "-q"); err != nil {
			return "", err
		}
	}
	// set-url fails if origin doesn't exist yet; add it then.
	if _, err := c.run(ctx, repoDir, env, redact, "remote", "set-url", "origin", site.Repo); err != nil {
		if _, err := c.run(ctx, repoDir, env, redact, "remote", "add", "origin", site.Repo); err != nil {
			return "", err
		}
	}
	if _, err := c.run(ctx, repoDir, env, redact, "fetch", "--depth=1", "--prune", "origin", site.Branch); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, repoDir, env, redact, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return "", err
	}
	// Hugo themes are commonly git submodules. The URL-scoped auth header
	// covers same-host private submodules; cross-host private submodules are
	// out of scope (documented). sync first: update --init reuses the URL
	// recorded in .git/config, so an upstream .gitmodules URL change would
	// otherwise be ignored forever.
	if _, err := c.run(ctx, repoDir, env, redact, "submodule", "sync", "--recursive", "--quiet"); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, repoDir, env, redact, "submodule", "update", "--init", "--recursive"); err != nil {
		return "", err
	}
	out, err := c.run(ctx, repoDir, env, redact, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitEnv builds the minimal environment for git: writable HOME, no global or
// system config, no terminal prompts, and — when the site has a token — an
// http.extraHeader scoped to the repo's origin, passed via GIT_CONFIG_*
// (invisible to ps, never on disk).
func (c *Client) gitEnv(site *config.Site) ([]string, error) {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + c.HomeDir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
	}
	if site.Token == "" {
		return env, nil
	}
	u, err := url.Parse(site.Repo)
	if err != nil {
		return nil, fmt.Errorf("parse repo url: %w", err)
	}
	header := "AUTHORIZATION: Basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+site.Token))
	env = append(env,
		"GIT_CONFIG_COUNT=1",
		fmt.Sprintf("GIT_CONFIG_KEY_0=http.%s://%s/.extraHeader", u.Scheme, u.Host),
		"GIT_CONFIG_VALUE_0="+header,
	)
	return env, nil
}

func (c *Client) run(ctx context.Context, dir string, env []string, redact func(string) string, args ...string) (string, error) {
	opCtx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		opCtx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(opCtx, c.GitBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > outputTailBytes {
			tail = tail[len(tail)-outputTailBytes:]
		}
		if opCtx.Err() != nil {
			err = fmt.Errorf("%w (%v)", opCtx.Err(), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, redact(strings.TrimSpace(tail)))
	}
	return string(out), nil
}

// newRedactor scrubs the raw token and its base64 header form from text.
func newRedactor(token string) func(string) string {
	if token == "" {
		return func(s string) string { return s }
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	r := strings.NewReplacer(token, "[REDACTED]", b64, "[REDACTED]")
	return r.Replace
}
