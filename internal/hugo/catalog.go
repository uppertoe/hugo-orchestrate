// Package hugo resolves site Hugo versions against the versions baked into
// the image at build time. The runtime never downloads Hugo; it only
// validates that requested versions exist under the binary root.
package hugo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Catalog is the set of installed Hugo versions and the manifest default.
type Catalog struct {
	Default  string
	Versions []string // includes Default, manifest order, deduped
	binRoot  string
}

// LoadCatalog parses the manifest at manifestPath (first non-comment line is
// the default version; remaining lines are additional allowed versions) and
// verifies each version's binary exists under binRoot/<version>/hugo.
func LoadCatalog(manifestPath, binRoot string) (*Catalog, error) {
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("open hugo manifest: %w", err)
	}
	defer f.Close()

	c := &Catalog{binRoot: binRoot}
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !versionRe.MatchString(line) {
			return nil, fmt.Errorf("hugo manifest %s: invalid version %q (want X.Y.Z)", manifestPath, line)
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		if c.Default == "" {
			c.Default = line
		}
		c.Versions = append(c.Versions, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read hugo manifest: %w", err)
	}
	if c.Default == "" {
		return nil, fmt.Errorf("hugo manifest %s lists no versions", manifestPath)
	}

	for _, v := range c.Versions {
		bin := c.BinaryPath(v)
		info, err := os.Stat(bin)
		if err != nil {
			return nil, fmt.Errorf("hugo %s listed in manifest but binary missing at %s: %w", v, bin, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return nil, fmt.Errorf("hugo %s at %s is not an executable file", v, bin)
		}
	}
	return c, nil
}

// BinaryPath returns the path of the hugo binary for a version (no checks).
func (c *Catalog) BinaryPath(version string) string {
	return filepath.Join(c.binRoot, version, "hugo")
}

// Resolve maps a site's requested version (or "" for the manifest default)
// to a binary path, failing if the version is not installed.
func (c *Catalog) Resolve(requested string) (version, binary string, err error) {
	if requested == "" {
		requested = c.Default
	}
	for _, v := range c.Versions {
		if v == requested {
			return v, c.BinaryPath(v), nil
		}
	}
	return "", "", fmt.Errorf("hugo version %s is not installed (installed: %s)",
		requested, strings.Join(c.Versions, ", "))
}
