package build

import (
	"os"
	"path/filepath"
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
