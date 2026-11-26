// Package linuxrelease reads a release's Linux host: agentsd-linux-<arch>-<version>.tar.gz,
// which holds agentsd and the MCP servers Agents ships, each beside its .sha256
// (release-host.sh). The control plane unpacks it for an install over SSH (#5787), and a
// Linux machine for its own update (#5788, epic #5785).
package linuxrelease

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Programs are the programs of a Linux host: the host (Go, #1136; one name since #1381);
// github-mcp (#1232), linear-mcp (#839), gitlab-mcp (#1629), email-mcp (#1780), jira-mcp
// (#3247), slack-mcp (#3475), googlechat-mcp (#1016), whatsapp-mcp (#1352), sentry-mcp
// (#3583), betterstack-mcp (#3655), forgejo-mcp (#1753), bitbucket-mcp (#2828),
// azuredevops-mcp (#2829), azureboards-mcp (#3804).
var Programs = []string{"agentsd", "github-mcp", "linear-mcp", "gitlab-mcp", "email-mcp", "jira-mcp", "slack-mcp",
	"googlechat-mcp", "whatsapp-mcp", "sentry-mcp", "betterstack-mcp", "forgejo-mcp", "bitbucket-mcp",
	"azuredevops-mcp", "azureboards-mcp"}

// Arches are the architectures a release is built for, as uname -m says them.
var Arches = []string{"x86_64", "aarch64"}

// FileNames are the only names of a Linux host's files: <program>-linux-<arch> and its
// .sha256, and VERSION.
var FileNames = func() map[string]bool {
	names := map[string]bool{"VERSION": true}
	for _, arch := range Arches {
		for _, program := range Programs {
			names[program+"-linux-"+arch] = true
			names[program+"-linux-"+arch+".sha256"] = true
		}
	}
	return names
}()

// Tarball is the release's file of the Linux host for arch.
func Tarball(arch, version string) string { return "agentsd-linux-" + arch + "-" + version + ".tar.gz" }

// Unpack puts the files for arch from a release's tarball into dir: only FileNames, only
// regular files, each at most limit bytes. A program whose .sha256 does not match it is
// left out, with its .sha256.
func Unpack(tarball, dir, arch string, limit int64) error {
	f, err := os.Open(tarball) //nolint:gosec // G304: a checked release file
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !FileNames[name] || !strings.Contains(name, "-linux-"+arch) {
			continue
		}
		// store-bounds: not a store: this unpacks release files into the caller's folder
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // G302, G304: a checked name in our own folder; public file
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(tr, limit+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if n > limit {
			return fmt.Errorf("%s is larger than %d bytes", name, limit)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		program := filepath.Join(dir, e.Name())
		if strings.HasSuffix(program, ".sha256") {
			continue
		}
		want, _ := os.ReadFile(program + ".sha256") //nolint:gosec // G304: our own folder
		if strings.TrimSpace(string(want)) != FileSum(program) {
			_ = os.Remove(program)
			_ = os.Remove(program + ".sha256")
		}
	}
	return nil
}

// FileSum is a file's SHA-256 in hex, or "" when it cannot be read.
func FileSum(path string) string {
	f, err := os.Open(path) //nolint:gosec // G304: our own folder
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
