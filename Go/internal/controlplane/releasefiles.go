package controlplane

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

	"github.com/agentloops-dev/agent-loops/Go/internal/linuxrelease"
	"github.com/agentloops-dev/agent-loops/Go/internal/releasesig"
	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// A Mac's self-update downloads its release from here first (#5786, epic #5785):
// GET /v1/releases/<version>/<file>, for every file of the release this control plane
// names (its own version). A Linux file that is in its server folder comes from there.
// Any other file comes from the public releases the first time a machine asks: it is
// checked against that release's SHA256SUMS, kept in ReleaseFolder, and served from there
// after that. SHA256SUMS.sig, which no checksum lists, is kept with SHA256SUMS and passed
// on as it is: the machine checks it too (releasesig). Only the files of its own release and the one before it are kept.
//
// A Linux machine's first install takes them too (#5787): GET /v1/releases/VERSION names
// the release for install.sh, and an install over SSH with no host for the machine in the
// server folder unpacks the release's agentsd-linux-<arch>-<version>.tar.gz (linux).

// ReleaseDownloads is where a release's files are, by tag (#1064: the public releases'
// repository, not the source's).
const ReleaseDownloads = "https://github.com/agentloops-dev/agents-releases/releases/download"

const (
	releaseFileLimit = 1 << 30 // the release's zip is about 100 MB
	releaseSumsLimit = 1 << 16
	releaseSumsName  = releasesig.SumsName
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
	if rest == "VERSION" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		// The release this control plane names (machine/release), for install.sh (#5787).
		if rf.version == "" {
			plain(w, http.StatusNotFound, "this control plane names no release\n")
		} else {
			plain(w, http.StatusOK, rf.version+"\n")
		}
		return
	}
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
	f, err := os.Open(path) //nolint:gosec // G703: path is built from a checked version and file name
	if err != nil {
		plain(w, http.StatusNotFound, "this control plane has no such file\n")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadWriteTimeout))
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
//
// SHA256SUMS and its signature are kept only as a pair that verifies. A release with no
// signature keeps SHA256SUMS alone, which serves a Mac. A folder that this control plane
// filled (fetchedMark) follows a release that was made again; one that a person filled is
// never changed, only added to.
func (rf *releaseFiles) fetch(ctx context.Context, version, name string) error {
	dir := filepath.Join(rf.folder, version)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G7xx: built from a checked version and file name, never a request path
		return err
	}
	base := rf.source + "/v" + version + "/"
	if err := rf.keepPair(ctx, base, dir, name == releasesig.SigName); err != nil {
		return err
	}
	if name == releaseSumsName || name == releasesig.SigName {
		if !fileExists(filepath.Join(dir, name)) {
			return os.ErrNotExist
		}
		return nil
	}
	err := rf.fetchListed(ctx, base, dir, name)
	if err == nil || !errors.Is(err, errOtherSum) || !fileExists(filepath.Join(dir, fetchedMark)) {
		return err
	}
	// The release was made again since its checksums were kept.
	if err := rf.renewPair(ctx, base, dir); err != nil {
		return err
	}
	return rf.fetchListed(ctx, base, dir, name)
}

// fetchedMark is in a release's folder whose SHA256SUMS this control plane fetched.
const fetchedMark = ".fetched"

var errOtherSum = errors.New("does not match its checksum")

// fetchListed takes a file that the kept SHA256SUMS lists.
func (rf *releaseFiles) fetchListed(ctx context.Context, base, dir, name string) error {
	text, err := os.ReadFile(filepath.Join(dir, releaseSumsName)) //nolint:gosec // G304: a kept release file
	if err != nil {
		return err
	}
	want := sumLine(string(text), name)
	if want == "" {
		return fmt.Errorf("%s lists no checksum for %s", base, name)
	}
	_, err = rf.download(ctx, base+name, filepath.Join(dir, name), releaseFileLimit, want)
	return err
}

// keepPair makes sure that dir has SHA256SUMS, and, for signature, SHA256SUMS.sig.
func (rf *releaseFiles) keepPair(ctx context.Context, base, dir string, signature bool) error {
	sums := filepath.Join(dir, releaseSumsName)
	if !fileExists(sums) || signature && fileExists(filepath.Join(dir, fetchedMark)) {
		return rf.renewPair(ctx, base, dir)
	}
	if !signature {
		return nil
	}
	// A person's SHA256SUMS takes the public signature only when it is the one of these sums.
	text, err := os.ReadFile(sums) //nolint:gosec // G304: a kept release file
	if err != nil {
		return err
	}
	sig, err := rf.text(ctx, base+releasesig.SigName, releasesig.Limit)
	if err != nil {
		return err
	}
	if err := releasesig.Verify(text, sig); err != nil {
		return err
	}
	return keepText(filepath.Join(dir, releasesig.SigName), sig)
}

// renewPair fetches SHA256SUMS and its signature and keeps both, or neither. It drops each
// kept file that the new sums do not match. A signed pair is never replaced by unsigned sums.
func (rf *releaseFiles) renewPair(ctx context.Context, base, dir string) error {
	sumsPath, sigPath := filepath.Join(dir, releaseSumsName), filepath.Join(dir, releasesig.SigName)
	sums, err := rf.text(ctx, base+releaseSumsName, releaseSumsLimit)
	if err != nil {
		return err
	}
	sig, err := rf.text(ctx, base+releasesig.SigName, releasesig.Limit)
	switch {
	case err == nil:
		if err := releasesig.Verify(sums, sig); err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
	case !errors.Is(err, os.ErrNotExist) || fileExists(sigPath):
		return err
	}
	if err := keepText(filepath.Join(dir, fetchedMark), nil); err != nil {
		return err
	}
	// A stop between these steps leaves SHA256SUMS with no signature, never with another's.
	if err := os.Remove(sigPath); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G7xx: built from a checked version and file name, never a request path
		return err
	}
	if err := keepText(sumsPath, sums); err != nil {
		return err
	}
	rf.dropOthers(dir, string(sums))
	if sig == nil {
		return nil
	}
	return keepText(sigPath, sig)
}

// dropOthers takes away the kept files that sums does not list as they are.
func (rf *releaseFiles) dropOthers(dir, sums string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == releaseSumsName || name == releasesig.SigName {
			continue
		}
		path := filepath.Join(dir, name)
		if e.IsDir() && strings.HasPrefix(name, unpackedPrefix) {
			_ = os.RemoveAll(path) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
		} else if e.Type().IsRegular() && sumLine(sums, name) != linuxrelease.FileSum(path) {
			_ = os.Remove(path) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
		}
	}
}

// text is a small file of the release; os.ErrNotExist when the source has none.
func (rf *releaseFiles) text(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
	if err != nil {
		return nil, err
	}
	resp, err := rf.http.Do(req) //nolint:gosec // G704: the url is the release source plus a checked version and file name
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", url, os.ErrNotExist)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return b, err
}

// keepText writes a small file in one step.
func keepText(path string, text []byte) error {
	// store-bounds: not a store: a release file's temporary copy, renamed or removed at once
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fetch-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(text)
	if err := errors.Join(err, tmp.Chmod(0o644), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
}

// download copies url to dest through a temporary file, and renames it into place when it
// is whole and, given a sum, matches it. It says the file's sum.
func (rf *releaseFiles) download(ctx context.Context, url, dest string, limit int64, want ...string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
	if err != nil {
		return "", err
	}
	resp, err := rf.http.Do(req) //nolint:gosec // G704: the url is the release source plus a checked version and file name
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", url, resp.Status)
	}
	// A release's file, renamed into place when it is whole; prune keeps this release and the one before.
	// store-bounds: kind=blob; count=2 releases; size=1GiB a file; age=until-deleted; applied=on download (prune, a larger file is refused) (#7352)
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
		return "", fmt.Errorf("%s %w", url, errOtherSum)
	}
	// A release file is public: G302 is no concern.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: public file
		return "", err
	}
	return sum, os.Rename(tmp.Name(), dest) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
}

// unpackedPrefix starts the folder of a release unpacked for an architecture, once its
// signature was good. (Before releases were signed it was linux-<arch>: not used again.)
const unpackedPrefix = "signed-linux-"

// linux is a folder with this control plane's release of the Linux host for arch (x86_64
// or aarch64), laid out as the server folder is (agentsd-linux-<arch> and the MCP
// servers, each with its .sha256, and VERSION), for an install over SSH when the server
// folder has none (#5787). The release's tarball is checked against SHA256SUMS and that
// against its signature, also when it was put in the folder by hand, and unpacked once
// beside it. Kept files that do not match are left, and the release is taken straight from
// the public releases.
func (rf *releaseFiles) linux(ctx context.Context, arch string) (string, error) {
	if rf.folder == "" || rf.version == "" {
		return "", errors.New("this control plane keeps no release files")
	}
	if arch != "x86_64" && arch != "aarch64" {
		return "", fmt.Errorf("no release is built for %s", arch)
	}
	dir := filepath.Join(rf.folder, rf.version, unpackedPrefix+arch)
	if fileExists(filepath.Join(dir, "VERSION")) {
		return dir, nil
	}
	name := linuxrelease.Tarball(arch, rf.version)
	tarball, err := rf.find(ctx, rf.version, name)
	if err == nil {
		err = rf.checkSigned(ctx, tarball)
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if fileExists(filepath.Join(dir, "VERSION")) {
		return dir, nil
	}
	if mkdir := os.MkdirAll(filepath.Dir(dir), 0o750); mkdir != nil {
		return "", mkdir
	}
	tmp, terr := os.MkdirTemp(filepath.Dir(dir), ".unpack-")
	if terr != nil {
		return "", terr
	}
	defer os.RemoveAll(tmp)
	if err != nil {
		var ferr error
		if tarball, ferr = rf.fetchSigned(ctx, tmp, name); ferr != nil {
			return "", fmt.Errorf("%w (and from the public releases: %w)", err, ferr)
		}
	}
	out := filepath.Join(tmp, "out")
	if err := os.Mkdir(out, 0o750); err != nil {
		return "", err
	}
	if err := linuxrelease.Unpack(tarball, out, arch, releaseFileLimit); err != nil {
		return "", err
	}
	if !fileExists(filepath.Join(out, "agentsd-linux-"+arch)) {
		return "", fmt.Errorf("release %s has no host for %s", rf.version, arch)
	}
	// The unpacked release's version, in the release's folder that prune keeps.
	// store-bounds: kind=blob; count=2 releases; size=1GiB a file; age=until-deleted; applied=on download (prune, a larger file is refused) (#7352)
	if err := os.WriteFile(filepath.Join(out, "VERSION"), []byte(rf.version+"\n"), 0o644); err != nil { //nolint:gosec // G306: public file
		return "", err
	}
	_ = os.RemoveAll(dir)
	return dir, os.Rename(out, dir)
}

// fetchSigned takes one file of this control plane's release from the public releases into
// dir, checked as checkSigned does, and keeps nothing else.
func (rf *releaseFiles) fetchSigned(ctx context.Context, dir, name string) (string, error) {
	base := rf.source + "/v" + rf.version + "/"
	sums, err := rf.text(ctx, base+releaseSumsName, releaseSumsLimit)
	if err != nil {
		return "", err
	}
	sig, err := rf.text(ctx, base+releasesig.SigName, releasesig.Limit)
	if err != nil {
		return "", err
	}
	if err := releasesig.Verify(sums, sig); err != nil {
		return "", err
	}
	want := sumLine(string(sums), name)
	if want == "" {
		return "", fmt.Errorf("%s lists no checksum for %s", base, name)
	}
	path := filepath.Join(dir, name)
	_, err = rf.download(ctx, base+name, path, releaseFileLimit, want)
	return path, err
}

// checkSigned checks a kept file of this control plane's release against SHA256SUMS, and
// SHA256SUMS against its signature.
func (rf *releaseFiles) checkSigned(ctx context.Context, path string) error {
	var texts [2][]byte
	for i, name := range []string{releaseSumsName, releasesig.SigName} {
		kept, err := rf.find(ctx, rf.version, name)
		if err != nil {
			return fmt.Errorf("release %s has no %s: %w", rf.version, name, err)
		}
		if texts[i], err = os.ReadFile(kept); err != nil { //nolint:gosec // G304: a kept release file
			return err
		}
	}
	if err := releasesig.Verify(texts[0], texts[1]); err != nil {
		return fmt.Errorf("release %s: %w", rf.version, err)
	}
	name := filepath.Base(path)
	if want := sumLine(string(texts[0]), name); want == "" || want != linuxrelease.FileSum(path) {
		return fmt.Errorf("%s does not match release %s's checksum", name, rf.version)
	}
	return nil
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
			_ = os.RemoveAll(filepath.Join(rf.folder, old))
		}
	}
}

// sumLine is the sum SHA256SUMS gives a file ("<hex>  <name>"), or "".
func sumLine(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == sha256.Size*2 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p) //nolint:gosec // G7xx: built from a checked version and file name, never a request path
	return err == nil && fi.Mode().IsRegular()
}
