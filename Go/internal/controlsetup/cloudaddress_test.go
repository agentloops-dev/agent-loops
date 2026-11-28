package controlsetup

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeGCP answers as GCP's metadata service does: only with the Metadata-Flavor header.
func fakeGCP(t *testing.T, ip string) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" ||
			r.URL.Path != "/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = w.Write([]byte(ip))
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// fakeAWS answers as IMDSv2 does: a token by PUT, then the address only with that token.
func fakeAWS(t *testing.T, ip string) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token" && r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "":
			_, _ = w.Write([]byte("tok-123"))
		case r.Method == http.MethodGet && r.URL.Path == "/latest/meta-data/public-ipv4" && r.Header.Get("X-aws-ec2-metadata-token") == "tok-123":
			_, _ = w.Write([]byte(ip + "\n"))
		default:
			http.Error(w, "no", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func fakeDigitalOcean(t *testing.T, ip string) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata/v1/interfaces/public/0/ipv4/address" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(ip))
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// notFound answers every path with 404, as another cloud's metadata service would.
func notFound(t *testing.T) string {
	s := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(s.Close)
	return s.URL
}

func TestCloudAddressAsksEachCloud(t *testing.T) {
	for _, c := range []struct {
		name string
		e    func(t *testing.T) cloudEndpoints
		want string
	}{
		{"gcp", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{gcp: fakeGCP(t, "34.1.2.3"), aws: notFound(t), digitalOcean: notFound(t)}
		}, "34.1.2.3"},
		{"aws", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{gcp: notFound(t), aws: fakeAWS(t, "54.1.2.3"), digitalOcean: notFound(t)}
		}, "54.1.2.3"},
		{"digitalocean", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{gcp: notFound(t), aws: notFound(t), digitalOcean: fakeDigitalOcean(t, "164.1.2.3")}
		}, "164.1.2.3"},
		// GCP's answer counts only with its Metadata-Flavor header back.
		{"gcp path without the header", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{gcp: fakeDigitalOcean(t, "34.1.2.3")}
		}, ""},
		// A private, loopback or malformed answer is no public address.
		{"private", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{digitalOcean: fakeDigitalOcean(t, "10.0.0.4")}
		}, ""},
		{"loopback", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{digitalOcean: fakeDigitalOcean(t, "127.0.0.1")}
		}, ""},
		{"ipv6", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{digitalOcean: fakeDigitalOcean(t, "2001:db8::7")}
		}, ""},
		{"not an address", func(t *testing.T) cloudEndpoints { return cloudEndpoints{digitalOcean: fakeDigitalOcean(t, "<html>")} }, ""},
		{"none", func(t *testing.T) cloudEndpoints {
			return cloudEndpoints{gcp: notFound(t), aws: notFound(t), digitalOcean: notFound(t)}
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := cloudAddress(context.Background(), c.e(t), 5*time.Second)
			if c.want == "" && got.IsValid() || c.want != "" && got.String() != c.want {
				t.Errorf("cloudAddress = %v, want %q", got, c.want)
			}
		})
	}
}

// A machine that is not on a cloud waits no longer than the time limit (#7638).
func TestCloudAddressWaitsNoLongerThanItsLimit(t *testing.T) {
	stop := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stop }))
	t.Cleanup(func() { close(stop); hang.Close() })
	start := time.Now()
	got := cloudAddress(context.Background(), cloudEndpoints{gcp: hang.URL, aws: hang.URL, digitalOcean: hang.URL}, 200*time.Millisecond)
	if got.IsValid() {
		t.Errorf("cloudAddress = %v, want none", got)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %v, want about 200ms", took)
	}
}

// The install asks the metadata service only after the machine's own public address and
// the seen uplink address, and takes its answer before the private one (#7638).
func TestInstallTakesTheCloudsAddressLast(t *testing.T) {
	private := netip.MustParseAddr("10.128.0.5")
	cloud := netip.MustParseAddr("34.1.2.3")
	for _, c := range []struct {
		name      string
		ip        netip.Addr
		public    bool
		seen      netip.Addr
		cloud     netip.Addr
		wantURL   string
		wantOut   string
		wantAsked bool
	}{
		{"behind NAT, never joined", private, false, netip.Addr{}, cloud, "https://34.1.2.3:8791", "takes 34.1.2.3, the public address its cloud's metadata service gives", true},
		{"no address, never joined", netip.Addr{}, false, netip.Addr{}, cloud, "https://34.1.2.3:8791", "reach this control plane at https://34.1.2.3:8791.", true},
		{"seen first", private, false, netip.MustParseAddr("198.51.100.9"), cloud, "https://198.51.100.9:8791", "its uplink came from", false},
		{"own public first", netip.MustParseAddr("203.0.113.7"), true, netip.Addr{}, cloud, "https://203.0.113.7:8791", "reach this control plane at https://203.0.113.7:8791.", false},
		{"not on a cloud", private, false, netip.Addr{}, netip.Addr{}, "https://10.128.0.5:8791", "private address", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, exe := setup(t)
			var out bytes.Buffer
			asked := false
			d := Deps{Systemd: func() bool { return true }, Run: (&fake{}).run, Out: &out,
				Address: func() (netip.Addr, bool) { return c.ip, c.public },
				Seen:    func() netip.Addr { return c.seen },
				Cloud:   func(context.Context) netip.Addr { asked = true; return c.cloud }}
			if err := Install(context.Background(), Options{Home: home, Executable: exe}, d); err != nil {
				t.Fatal(err)
			}
			unit, err := os.ReadFile(LayoutOf(home).Unit)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(unit), "Environment=AGENTS_CONTROL_URL="+c.wantURL+"\n") {
				t.Errorf("unit's address, want %q:\n%s", c.wantURL, unit)
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Errorf("output lacks %q:\n%s", c.wantOut, out.String())
			}
			if asked != c.wantAsked {
				t.Errorf("asked the metadata service = %v, want %v", asked, c.wantAsked)
			}
		})
	}
}
