package controlplane

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// A Mac's self-update downloads its release from here first (#5786, epic #5785):
// GET /v1/releases/<version>/<file>, for every file of the release this control plane
// names (its own version). A Linux file that is in its server folder comes from there.
// Any other file comes from the public releases the first time a machine asks: it is
// checked against that release's SHA256SUMS, kept in ReleaseFolder, and served from there
// after that. Only the files of its own release and the one before it are kept.

// ReleaseDownloads is where a release's files are, by tag (#1064: the public releases'
// repository, not the source's).
const ReleaseDownloads = "https://github.com/agentloops-dev/agents-releases/releases/download"

const (
	releaseFileLimit = 1 << 30 // the release's zip is about 100 MB
	releaseSumsLimit = 1 << 16
	releaseSumsName  = "SHA256SUMS"
)

var (
	releaseVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	releaseFilePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// releaseFiles keeps the public releases' files for the Macs of this control plane.
type releaseFiles struct {
	folder  string // "" keeps none: only the server folder answers
	source  string // ReleaseDownloads, or what a test says
	version string // this control plane's release; "" for a development build
	server  string // ServerFolder

	mu   sync.Mutex // one download at a time: a release is fetched once, not once per Mac
	http *http.Client
}

func newReleaseFiles(cfg Config) *releaseFiles {
	version := cfg.Version
	if strings.ContainsAny(version, "-+") {
		version = ""
	}
	source := cfg.ReleaseSource
	if source == "" {
		source = ReleaseDownloads
	}
	return &releaseFiles{folder: cfg.ReleaseFolder, source: strings.TrimSuffix(source, "/"), version: version,
		server: cfg.ServerFolder, http: &http.Client{Timeout: 30 * time.Minute}}
}

// serve answers GET /v1/releases/<version>/<file>.
func (rf *releaseFiles) serve(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/releases/")
	version, name, ok := strings.Cut(rest, "/")
	if !ok || r.Method != http.MethodGet && r.Method != http.MethodHead ||
		!releaseVersionPattern.MatchString(version) || !releaseFilePattern.MatchString(name) {
		plain(w, http.StatusNotFound, "this control plane has no such file\n")
		return
	}
	path, err := rf.find(r.Context(), version, name)
	if err != nil {
		plain(w, http.StatusNotFound, "this control plane has no such file\n")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		plain(w, http.StatusNotFound, "this control plane has no such file\n")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadWriteTimeout))
	http.ServeContent(w, r, name, time.Time{}, f)
}

// find is the path of a release's file: the server folder's own, a kept copy, or one
// fetched now from the public releases (only for this control plane's own release).
func (rf *releaseFiles) find(ctx context.Context, version, name string) (string, error) {
	if version == rf.version && rf.server != "" && serverFileNames[name] {
		if p := filepath.Join(rf.server, name); fileExists(p) {
			return p, nil
		}
	}
	if rf.folder == "" {
		return "", os.ErrNotExist
	}
	kept := filepath.Join(rf.folder, version, name)
	if fileExists(kept) {
		return kept, nil
	}
	if version != rf.version || rf.version == "" {
		return "", os.ErrNotExist
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if fileExists(kept) {
		return kept, nil
	}
	if err := rf.fetch(ctx, version, name); err != nil {
		return "", err
	}
	safe.Go("release-prune", rf.prune)
	return kept, nil
}

// fetch takes a file from the public releases, checks it against the release's
// SHA256SUMS, and keeps it. A file whose sum does not match is not kept.
func (rf *releaseFiles) fetch(ctx context.Context, version, name string) error {
	dir := filepath.Join(rf.folder, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := rf.source + "/v" + version + "/"
	sums := filepath.Join(dir, releaseSumsName)
	if name == releaseSumsName {
		_, err := rf.download(ctx, base+name, sums, releaseSumsLimit)
		return err
	}
	if !fileExists(sums) {
		if _, err := rf.download(ctx, base+releaseSumsName, sums, releaseSumsLimit); err != nil {
			return err
		}
	}
	text, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	want := sumLine(string(text), name)
	if want == "" {
		return fmt.Errorf("release %s lists no checksum for %s", version, name)
	}
	_, err = rf.download(ctx, base+name, filepath.Join(dir, name), releaseFileLimit, want)
	return err
}

// download copies url to dest through a temporary file, and renames it into place when it
// is whole and, given a sum, matches it. It says the file's sum.
func (rf *releaseFiles) download(ctx context.Context, url, dest string, limit int64, want ...string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := rf.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".fetch-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if len(want) > 0 && sum != want[0] {
		return "", fmt.Errorf("%s does not match its checksum", url)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	return sum, os.Rename(tmp.Name(), dest)
}

// prune keeps the folders of this release and of the newest one before it.
func (rf *releaseFiles) prune() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	entries, err := os.ReadDir(rf.folder)
	if err != nil {
		return
	}
	var others []string
	for _, e := range entries {
		if e.IsDir() && releaseVersionPattern.MatchString(e.Name()) && e.Name() != rf.version {
			others = append(others, e.Name())
		}
	}
	slices.SortFunc(others, compareVersions)
	if len(others) > 1 {
		for _, old := range others[:len(others)-1] {
			os.RemoveAll(filepath.Join(rf.folder, old))
		}
	}
}

// sumLine is the sum SHA256SUMS gives a file ("<hex>  <name>"), or "".
func sumLine(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
