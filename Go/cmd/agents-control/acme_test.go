package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestACMETLSServesHTTP1AndTheChallenge(t *testing.T) {
	c := acmeTLS("agents.example.com", "me@example.com", filepath.Join(t.TempDir(), "acme"))
	if !slices.Equal(c.NextProtos, []string{"http/1.1", "acme-tls/1"}) {
		t.Errorf("NextProtos = %v", c.NextProtos)
	}
	if c.GetCertificate == nil {
		t.Error("no certificate source")
	}
}
