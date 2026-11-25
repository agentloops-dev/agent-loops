package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// /v1/releases/<version>/<file> serves the public releases' files, kept (#5786).

func releaseRig(t *testing.T, version string, files map[string]string, sumOf map[string]string) (*releaseFiles, *atomic.Int64) {
	t.Helper()
	t.Setenv("AGENTS_RELEASE_KEY", "")
	var hits atomic.Int64
	sums := ""
	for name, body := range files {
		if s, ok := sumOf[name]; ok {
			body = s
		}
		sum := sha256.Sum256([]byte(body))
		sums += hex.EncodeToString(sum[:]) + "  " + name + "\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		dir := "/v" + version + "/"
		if r.URL.Path == dir+"SHA256SUMS" {
			_, _ = io.WriteString(w, sums)
			return
		}
		if _, ok := files["SHA256SUMS.sig"]; !ok && r.URL.Path == dir+"SHA256SUMS.sig" {
			_, _ = io.WriteString(w, releaseSig(t, "current", sums))
			return
		}
		for name, body := range files {
			if body == "" {
				continue
			}
			if r.URL.Path == dir+name {
				_, _ = io.WriteString(w, body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	rf := newReleaseFiles(Config{Version: version, ReleaseFolder: t.TempDir(), ReleaseSource: srv.URL})
	return rf, &hits
}

func getRelease(rf *releaseFiles, version, name string) (int, string) {
	rec := httptest.NewRecorder()
	rf.serve(rec, httptest.NewRequest(http.MethodGet, "/v1/releases/"+version+"/"+name, nil))
	return rec.Code, rec.Body.String()
}

func TestReleaseFileIsFetchedOnceAndKept(t *testing.T) {
	rf, hits := releaseRig(t, "0.12.0", map[string]string{"Agent-Loops-0.12.0.zip": "zip bytes"}, nil)
	for range 2 {
		if code, body := getRelease(rf, "0.12.0", "Agent-Loops-0.12.0.zip"); code != 200 || body != "zip bytes" {
			t.Fatalf("%d %q", code, body)
		}
	}
	if hits.Load() != 2 { // SHA256SUMS and the zip, once
		t.Fatalf("%d requests", hits.Load())
	}
	if code, _ := getRelease(rf, "0.12.0", "SHA256SUMS"); code != 200 {
		t.Fatal(code)
	}
}

func TestReleaseFileWithABadSumIsRefusedAndNotKept(t *testing.T) {
	rf, _ := releaseRig(t, "0.12.0", map[string]string{"a.zip": "real"}, map[string]string{"a.zip": "other"})
	if code, _ := getRelease(rf, "0.12.0", "a.zip"); code != 404 {
		t.Fatal(code)
	}
	if fileExists(filepath.Join(rf.folder, "0.12.0", "a.zip")) {
		t.Fatal("kept")
	}
}

func TestReleaseFileOnlyForItsOwnRelease(t *testing.T) {
	rf, hits := releaseRig(t, "0.12.0", map[string]string{"a.zip": "x"}, nil)
	for _, p := range [][2]string{{"0.13.0", "a.zip"}, {"0.12.0", "../a.zip"}, {"0.12.0", "missing.zip"}, {"x", "a.zip"}} {
		if code, _ := getRelease(rf, p[0], p[1]); code != 404 {
			t.Fatalf("%v: %d", p, code)
		}
	}
	if hits.Load() > 2 {
		t.Fatalf("%d requests", hits.Load())
	}
}

func TestReleaseFilesKeepOwnAndTheOneBefore(t *testing.T) {
	rf, _ := releaseRig(t, "0.12.0", map[string]string{"a.zip": "x"}, nil)
	for _, v := range []string{"0.9.0", "0.10.0", "0.11.0"} {
		_ = os.MkdirAll(filepath.Join(rf.folder, v), 0o750)
	}
	if code, _ := getRelease(rf, "0.12.0", "a.zip"); code != 200 {
		t.Fatal(code)
	}
	rf.prune()
	for v, want := range map[string]bool{"0.9.0": false, "0.10.0": false, "0.11.0": true, "0.12.0": true} {
		if _, err := os.Stat(filepath.Join(rf.folder, v)); (err == nil) != want {
			t.Errorf("%s kept: %v", v, err == nil)
		}
	}
}
