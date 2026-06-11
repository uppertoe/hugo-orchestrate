package publish

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mkBuild(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"index.html":   content,
		"css/main.css": "body{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readLive(t *testing.T, liveDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(liveDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishFirstAndReplace(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "out", "docs")

	p := New()
	if err := p.Publish(mkBuild(t, root, "b1", "v1"), live, "b1"); err != nil {
		t.Fatal(err)
	}
	if got := readLive(t, live); got != "v1" {
		t.Errorf("live = %q", got)
	}
	if err := p.Publish(mkBuild(t, root, "b2", "v2"), live, "b2"); err != nil {
		t.Fatal(err)
	}
	if got := readLive(t, live); got != "v2" {
		t.Errorf("live = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "out"))
	if len(entries) != 1 {
		t.Errorf("expected only live dir to remain, got %v", entries)
	}
}

func TestPublishEXDEVFallbackStagesOnDestination(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "out", "docs")
	build := mkBuild(t, root, "b1", "v1")

	exdev := &os.LinkError{Op: "rename", Old: build, New: "", Err: syscall.EXDEV}
	var stagedVia string
	p := NewWithRename(func(oldpath, newpath string) error {
		if oldpath == build {
			return exdev // source rename crosses devices
		}
		stagedVia = oldpath // final swap source must be the staging dir
		return os.Rename(oldpath, newpath)
	})

	if err := p.Publish(build, live, "b1"); err != nil {
		t.Fatal(err)
	}
	if got := readLive(t, live); got != "v1" {
		t.Errorf("live = %q", got)
	}
	if !strings.Contains(stagedVia, ".tmp-b1") || filepath.Dir(stagedVia) != filepath.Dir(live) {
		t.Errorf("swap source %q is not a staging dir on the destination filesystem", stagedVia)
	}
	if _, err := os.Stat(build); err != nil {
		t.Errorf("EXDEV copy should leave the source build dir in place: %v", err)
	}
}

func TestPublishRollbackRestoresPrevious(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "out", "docs")
	p := New()
	if err := p.Publish(mkBuild(t, root, "b1", "v1"), live, "b1"); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("simulated swap failure")
	failing := NewWithRename(func(oldpath, newpath string) error {
		if newpath == live && strings.Contains(oldpath, ".tmp-") {
			return boom
		}
		return os.Rename(oldpath, newpath)
	})
	err := failing.Publish(mkBuild(t, root, "b2", "v2"), live, "b2")
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected swap failure, got %v", err)
	}
	if got := readLive(t, live); got != "v1" {
		t.Errorf("previous version not restored, live = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "out"))
	if len(entries) != 1 {
		t.Errorf("rollback left debris: %v", entries)
	}
}

func TestSweepOrphans(t *testing.T) {
	out := t.TempDir()
	for _, d := range []string{"docs", "docs.tmp-abc", "blog.__prev"} {
		if err := os.MkdirAll(filepath.Join(out, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := SweepOrphans(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v", removed)
	}
	if _, err := os.Stat(filepath.Join(out, "docs")); err != nil {
		t.Error("live dir must survive sweep")
	}
	if _, err := SweepOrphans(filepath.Join(out, "missing")); err != nil {
		t.Errorf("missing output root should not error: %v", err)
	}
}

func TestPruneBuilds(t *testing.T) {
	builds := t.TempDir()
	for i, name := range []string{"old1", "old2", "new1", "new2", "new3"} {
		dir := filepath.Join(builds, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i-5) * time.Hour)
		if err := os.Chtimes(dir, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneBuilds(builds, 3); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(builds)
	if len(entries) != 3 {
		t.Fatalf("kept %d dirs, want 3", len(entries))
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "old") {
			t.Errorf("pruned wrong dir, %s survived", e.Name())
		}
	}
	if err := PruneBuilds(filepath.Join(builds, "missing"), 3); err != nil {
		t.Errorf("missing builds dir should not error: %v", err)
	}
}
