package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOutput(t *testing.T) {
	empty := t.TempDir()
	if err := ValidateOutput(empty); err == nil {
		t.Error("empty dir must fail validation")
	}

	onlyDirs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(onlyDirs, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(onlyDirs); err == nil {
		t.Error("dir tree without files must fail validation")
	}

	ok := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ok, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ok, "sub", "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(ok); err != nil {
		t.Errorf("valid output rejected: %v", err)
	}

	if err := ValidateOutput(filepath.Join(ok, "missing")); err == nil {
		t.Error("missing dir must fail validation")
	}
}

// The hugo subprocess runs with a fixed env allowlist. GOMEMLIMIT joins it
// only when a ceiling is configured, so an unset limit leaves hugo exactly as
// it was; a set one caps its heap growth on a small host.
func TestRunPassesMemoryLimitToHugo(t *testing.T) {
	for _, tc := range []struct{ name, limit, want string }{
		{"unset", "", ""},
		{"set", "768MiB", "GOMEMLIMIT=768MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			envFile := filepath.Join(dir, "env.txt")
			fake := filepath.Join(dir, "hugo.sh")
			script := "#!/bin/sh\nenv > " + envFile + "\ntouch " + filepath.Join(dir, "out", "index.html") + "\n"
			if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			r := &Runner{HomeDir: dir, MemoryLimit: tc.limit}
			in := Input{
				Binary:      fake,
				SourceDir:   dir,
				DestDir:     filepath.Join(dir, "out"),
				CacheDir:    filepath.Join(dir, "cache"),
				Environment: "production",
			}
			if err := r.Run(context.Background(), in); err != nil {
				t.Fatalf("run: %v", err)
			}
			got, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if strings.Contains(string(got), "GOMEMLIMIT") {
					t.Errorf("no limit configured but hugo saw GOMEMLIMIT:\n%s", got)
				}
				return
			}
			if !strings.Contains(string(got), tc.want) {
				t.Errorf("hugo env missing %q:\n%s", tc.want, got)
			}
		})
	}
}
