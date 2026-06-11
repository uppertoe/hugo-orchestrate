package hugo

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hugo-manifest.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func installFake(t *testing.T, root string, versions ...string) {
	t.Helper()
	for _, v := range versions {
		dir := filepath.Join(root, v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hugo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadCatalog(t *testing.T) {
	root := t.TempDir()
	installFake(t, root, "0.155.3", "0.140.0")
	manifest := writeManifest(t, "# default first\n0.155.3\n0.140.0\n0.155.3\n")

	c, err := LoadCatalog(manifest, root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Default != "0.155.3" {
		t.Errorf("default = %s", c.Default)
	}
	if len(c.Versions) != 2 {
		t.Errorf("expected dedupe, got %v", c.Versions)
	}

	v, bin, err := c.Resolve("")
	if err != nil || v != "0.155.3" || bin != filepath.Join(root, "0.155.3", "hugo") {
		t.Errorf("resolve default: %s %s %v", v, bin, err)
	}
	if _, _, err := c.Resolve("0.999.0"); err == nil {
		t.Error("expected error for uninstalled version")
	}
}

func TestLoadCatalogFailures(t *testing.T) {
	root := t.TempDir()
	installFake(t, root, "0.155.3")

	if _, err := LoadCatalog(writeManifest(t, "0.155.3\n0.150.0\n"), root); err == nil {
		t.Error("expected error for missing binary")
	}
	if _, err := LoadCatalog(writeManifest(t, "not-a-version\n"), root); err == nil {
		t.Error("expected error for invalid version")
	}
	if _, err := LoadCatalog(writeManifest(t, "# only comments\n"), root); err == nil {
		t.Error("expected error for empty manifest")
	}
}
