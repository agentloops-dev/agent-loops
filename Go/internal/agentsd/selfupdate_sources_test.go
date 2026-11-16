package agentsd

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A self-update takes its release from the control plane first, and from the public
// releases when the control plane does not answer or has no such file (#5786).

// releasesServer serves release 0.12.0 under prefix, with the sum of sumOf and the bytes of
// zip, and counts its requests.
func releasesServer(t *testing.T, prefix string, sumOf, zip []byte, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(sumOf)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		switch req.URL.Path {
		case prefix + "SHA256SUMS":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  Agent-Loops-0.12.0.zip\n"))
		case prefix + "Agent-Loops-0.12.0.zip":
			w.Write(zip)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sourcesRig is a rig with no AGENTS_RELEASE_DOWNLOADS, a control plane at control and the
// public releases at public.
func sourcesRig(t *testing.T, control, public string) *selfUpdateRig {
	t.Helper()
	r := newSelfUpdateRig(t, []byte("zip"))
	t.Setenv("AGENTS_RELEASE_DOWNLOADS", "")
	if control != "" {
		r.u.control = func() []releaseSource { return []releaseSource{{base: control}} }
	}
	r.u.public = public
	return r
}

func TestSelfUpdateDownloadsFromTheControlPlane(t *testing.T) {
	var fromControl, fromPublic atomic.Int64
	control := releasesServer(t, "/v1/releases/0.12.0/", []byte("zip"), []byte("zip"), &fromControl)
	public := releasesServer(t, "/v0.12.0/", []byte("zip"), []byte("zip"), &fromPublic)
	r := sourcesRig(t, control.URL, public.URL)
	r.u.Named("0.12.0")
	r.restarted(t)
	if r.which(t) != "new" || fromControl.Load() != 2 || fromPublic.Load() != 0 {
		t.Fatalf("%s control %d public %d", r.which(t), fromControl.Load(), fromPublic.Load())
	}
}

func TestSelfUpdateFallsBackWhenTheControlPlaneIsDown(t *testing.T) {
	var fromPublic atomic.Int64
	public := releasesServer(t, "/v0.12.0/", []byte("zip"), []byte("zip"), &fromPublic)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	r := sourcesRig(t, down.URL, public.URL)
	r.u.Named("0.12.0")
	r.restarted(t)
	if r.which(t) != "new" || fromPublic.Load() != 2 {
		t.Fatalf("%s public %d", r.which(t), fromPublic.Load())
	}
}

func TestSelfUpdateFallsBackOnA404(t *testing.T) {
	var fromControl, fromPublic atomic.Int64
	control := releasesServer(t, "/nothing/", nil, nil, &fromControl) // answers 404 to the release
	public := releasesServer(t, "/v0.12.0/", []byte("zip"), []byte("zip"), &fromPublic)
	r := sourcesRig(t, control.URL, public.URL)
	r.u.Named("0.12.0")
	r.restarted(t)
	if r.which(t) != "new" || fromControl.Load() == 0 || fromPublic.Load() != 2 {
		t.Fatalf("%s control %d public %d", r.which(t), fromControl.Load(), fromPublic.Load())
	}
}

func TestSelfUpdateRefusesABadSumFromEitherSource(t *testing.T) {
	for _, from := range []string{"control", "public"} {
		t.Run(from, func(t *testing.T) {
			var hits atomic.Int64
			good := []byte("zip")
			bad := releasesServer(t, "/v1/releases/0.12.0/", good, []byte("tampered"), &hits)
			if from == "public" {
				bad = releasesServer(t, "/v0.12.0/", good, []byte("tampered"), &hits)
			}
			control, public := bad.URL, bad.URL
			if from == "control" {
				// The public releases are good, and still not tried after a refusal.
				var h atomic.Int64
				public = releasesServer(t, "/v0.12.0/", good, good, &h).URL
			} else {
				control = ""
			}
			r := sourcesRig(t, control, public)
			r.u.Named("0.12.0")
			s := r.until(t, wire.SelfUpdateStateFailed)
			if s.Message == nil || !strings.Contains(*s.Message, "checksum") || r.which(t) != "old" {
				t.Fatalf("%+v", s)
			}
		})
	}
}
