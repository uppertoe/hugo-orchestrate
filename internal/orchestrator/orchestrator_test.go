package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
	"github.com/uppertoe/hugo-orchestrate/internal/hugo"
	"github.com/uppertoe/hugo-orchestrate/internal/state"
)

// fakeHugo writes a minimal site into whatever --destination it is given,
// copying the checkout's content/ dir so tests can assert on commit content.
const fakeHugo = `#!/bin/sh
dest=""
prev=""
for a in "$@"; do
  [ "$prev" = "--destination" ] && dest="$a"
  prev="$a"
done
mkdir -p "$dest"
cp -R content/. "$dest"/ 2>/dev/null
echo "<html>built</html>" > "$dest/index.html"
`

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-c", "user.email=test@example.com",
		"-c", "user.name=test",
		"-c", "protocol.file.allow=always",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestEndToEndBuild exercises the full pipeline against a local file:// git
// repo and a stub hugo binary: sync → build → validate → publish → state.
func TestEndToEndBuild(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()

	// Source repository with one content file.
	repo := filepath.Join(root, "src-repo")
	if err := os.MkdirAll(filepath.Join(repo, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "content", "page.html"), []byte("rev1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q", "-b", "main")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "rev1")

	// Stub hugo + manifest.
	binRoot := filepath.Join(root, "hugo-bin")
	if err := os.MkdirAll(filepath.Join(binRoot, "0.155.3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binRoot, "0.155.3", "hugo"), []byte(fakeHugo), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "hugo-manifest.txt")
	if err := os.WriteFile(manifest, []byte("0.155.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	staticRoot := filepath.Join(root, "static")
	env, err := config.LoadEnv(func(k string) string {
		return map[string]string{
			"ORCH_STATIC_ROOT":        staticRoot,
			"ORCH_HUGO_MANIFEST_PATH": manifest,
			"ORCH_HUGO_BIN_ROOT":      binRoot,
			"ORCH_GIT_TIMEOUT":        "30s",
			"ORCH_BUILD_TIMEOUT":      "30s",
			"ORCH_OPERATION_RETRIES":  "0",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := hugo.LoadCatalog(manifest, binRoot)
	if err != nil {
		t.Fatal(err)
	}
	layout := config.NewLayout(env)
	states, err := state.NewStore(layout.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	site := &config.Site{
		Slug:       "docs",
		Repo:       "file://" + repo,
		Branch:     "main",
		PublishDir: "docs",
		HugoEnv:    "production",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch, err := New(env, []*config.Site{site}, catalog, states, log)
	if err != nil {
		t.Fatal(err)
	}

	orch.BuildSite(context.Background(), "docs", "startup")

	st, err := states.Read("docs")
	if err != nil || st == nil {
		t.Fatalf("state read: %v %v", st, err)
	}
	if st.Status != "success" {
		t.Fatalf("build failed: %+v", st)
	}
	if st.Commit == "" || st.Reason != "startup" {
		t.Errorf("state incomplete: %+v", st)
	}
	live := layout.LiveDir("docs")
	if data, err := os.ReadFile(filepath.Join(live, "page.html")); err != nil || string(data) != "rev1" {
		t.Fatalf("published content wrong: %q %v", data, err)
	}

	// Second build after a new commit replaces the live content atomically.
	if err := os.WriteFile(filepath.Join(repo, "content", "page.html"), []byte("rev2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "commit", "-aqm", "rev2")
	orch.BuildSite(context.Background(), "docs", "webhook")

	st2, _ := states.Read("docs")
	if st2.Status != "success" || st2.Commit == st.Commit {
		t.Fatalf("second build state wrong: %+v", st2)
	}
	if data, _ := os.ReadFile(filepath.Join(live, "page.html")); string(data) != "rev2" {
		t.Errorf("live content not updated: %q", data)
	}

	// WaitFirstPass must already be satisfied.
	if err := orch.WaitFirstPass(context.Background()); err != nil {
		t.Errorf("WaitFirstPass: %v", err)
	}
}
