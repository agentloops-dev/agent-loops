package controlsetup

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// CloudWait bounds the whole metadata lookup, so a machine that is not on a cloud waits
// no longer than this for an install (#7638).
const CloudWait = 1500 * time.Millisecond

// metadataHost is the link-local address of the metadata service on GCP, AWS and
// DigitalOcean. GCP's own name, metadata.google.internal, resolves to it there; the
// address needs no DNS lookup, which off a cloud can take longer than the lookup itself.
const metadataHost = "http://169.254.169.254"

// cloudEndpoints are the metadata services the lookup asks, each by its base URL.
type cloudEndpoints struct {
	gcp, aws, digitalOcean string
}

var realClouds = cloudEndpoints{gcp: metadataHost, aws: metadataHost, digitalOcean: metadataHost}

// CloudAddress is the public IPv4 address that the cloud's metadata service gives this
// VM (GCP, AWS IMDSv2, DigitalOcean), or an invalid one when no service answers with a
// public address within CloudWait. A VM behind NAT has no public address on an interface:
// only the metadata service knows it (#7638).
func CloudAddress(ctx context.Context) netip.Addr {
	return cloudAddress(ctx, realClouds, CloudWait)
}

func cloudAddress(ctx context.Context, e cloudEndpoints, wait time.Duration) netip.Addr {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	// No proxy: a proxy cannot reach this VM's link-local metadata service, and must not
	// be asked for it.
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	asks := []func() netip.Addr{
		func() netip.Addr { return askGCP(ctx, client, e.gcp) },
		func() netip.Addr { return askAWS(ctx, client, e.aws) },
		func() netip.Addr { return askDigitalOcean(ctx, client, e.digitalOcean) },
	}
	// Ask all at once, so a machine off a cloud waits for one time limit, not three.
	answers := make(chan netip.Addr, len(asks))
	for _, ask := range asks {
		go func() { answers <- ask() }()
	}
	for range asks {
		if a := <-answers; a.IsValid() {
			return a
		}
	}
	return netip.Addr{}
}

func askGCP(ctx context.Context, c *http.Client, base string) netip.Addr {
	if base == "" {
		return netip.Addr{}
	}
	body, h := get(ctx, c, http.MethodGet, base+"/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip",
		map[string]string{"Metadata-Flavor": "Google"})
	if h.Get("Metadata-Flavor") != "Google" {
		return netip.Addr{}
	}
	return publicIPv4(body)
}

func askAWS(ctx context.Context, c *http.Client, base string) netip.Addr {
	if base == "" {
		return netip.Addr{}
	}
	token, _ := get(ctx, c, http.MethodPut, base+"/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"})
	if token == "" {
		return netip.Addr{}
	}
	body, _ := get(ctx, c, http.MethodGet, base+"/latest/meta-data/public-ipv4",
		map[string]string{"X-aws-ec2-metadata-token": token})
	return publicIPv4(body)
}

func askDigitalOcean(ctx context.Context, c *http.Client, base string) netip.Addr {
	if base == "" {
		return netip.Addr{}
	}
	body, _ := get(ctx, c, http.MethodGet, base+"/metadata/v1/interfaces/public/0/ipv4/address", nil)
	return publicIPv4(body)
}

// get is the trimmed body and the headers of a 200 answer, or "" and none.
func get(ctx context.Context, c *http.Client, method, url string, header map[string]string) (string, http.Header) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return "", http.Header{}
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", http.Header{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", http.Header{}
	}
	// An address or a token is short; read no more than a small bound.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", http.Header{}
	}
	return strings.TrimSpace(string(b)), resp.Header
}

// publicIPv4 is s as a public IPv4 address, or an invalid one.
func publicIPv4(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || !a.IsGlobalUnicast() || a.IsPrivate() {
		return netip.Addr{}
	}
	return a
}
