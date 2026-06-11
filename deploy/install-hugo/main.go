// Command install-hugo downloads, verifies and installs the Hugo versions
// listed in a manifest. It runs at image build time only — the runtime image
// is read-only and offline, with versions baked under /opt/hugo/<version>/hugo.
//
// Manifest format: first non-comment line is the default version, remaining
// lines are additional allowed versions (X.Y.Z, '#' comments allowed).
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func main() {
	manifest := flag.String("manifest", "/etc/orchestrator/hugo-manifest.txt", "path to hugo-manifest.txt")
	dest := flag.String("dest", "/opt/hugo", "install root; binaries land at <dest>/<version>/hugo")
	arch := flag.String("arch", "", "target architecture: amd64 or arm64")
	flavor := flag.String("flavor", "extended", "hugo flavor: extended or standard")
	flag.Parse()

	if *arch != "amd64" && *arch != "arm64" {
		fatal("--arch must be amd64 or arm64, got %q", *arch)
	}
	versions, err := readManifest(*manifest)
	if err != nil {
		fatal("%v", err)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	for _, v := range versions {
		if err := install(client, v, *arch, *flavor, *dest); err != nil {
			fatal("hugo %s: %v", v, err)
		}
		fmt.Printf("installed hugo %s (%s, %s)\n", v, *flavor, *arch)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "install-hugo: "+format+"\n", args...)
	os.Exit(1)
}

func readManifest(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var versions []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !versionRe.MatchString(line) {
			return nil, fmt.Errorf("manifest %s: invalid version %q", path, line)
		}
		if !seen[line] {
			seen[line] = true
			versions = append(versions, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("manifest %s lists no versions", path)
	}
	return versions, nil
}

func install(client *http.Client, version, arch, flavor, destRoot string) error {
	base := fmt.Sprintf("https://github.com/gohugoio/hugo/releases/download/v%s", version)
	prefix := "hugo"
	if flavor == "extended" {
		prefix = "hugo_extended"
	}
	tarball := fmt.Sprintf("%s_%s_linux-%s.tar.gz", prefix, version, arch)

	sums, err := fetch(client, fmt.Sprintf("%s/hugo_%s_checksums.txt", base, version))
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	wantSum, err := findChecksum(string(sums), tarball)
	if err != nil {
		return err
	}

	archive, err := fetch(client, base+"/"+tarball)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", tarball, err)
	}
	gotSum := sha256.Sum256(archive)
	if hex.EncodeToString(gotSum[:]) != wantSum {
		return fmt.Errorf("checksum mismatch for %s: got %x, want %s", tarball, gotSum, wantSum)
	}

	return extractHugo(archive, filepath.Join(destRoot, version, "hugo"))
}

func fetch(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func findChecksum(sums, filename string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", filename)
}

func extractHugo(archive []byte, destPath string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive contains no hugo binary")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "hugo" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}
