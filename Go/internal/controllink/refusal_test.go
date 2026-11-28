package controllink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An address that refuses the upgrade with Agents-Refusal says why, and its line (#6785).
func TestDialReadsAnAddressRefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
		wait   time.Duration
	}{{http.StatusForbidden, Suspended, suspendedWait}, {http.StatusGone, Gone, goneWait}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Agents-Refusal", tc.reason)
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("This hosted account is " + tc.reason + ".\n"))
		}))
		_, err := Dial(context.Background(), srv.URL, "")
		srv.Close()
		var r *AddressRefusal
		if !errors.As(err, &r) {
			t.Fatalf("%s: got %v, want an AddressRefusal", tc.reason, err)
		}
		if r.Status != tc.status || r.Reason != tc.reason || r.Line != "This hosted account is "+tc.reason+"." {
			t.Fatalf("%s: got %+v", tc.reason, r)
		}
		if err.Error() != r.Line {
			t.Fatalf("%s: the error says %q, not its line", tc.reason, err)
		}
		if got := (&Uplink{}).refusedWait(err); got != tc.wait {
			t.Fatalf("%s: waits %s, want %s", tc.reason, got, tc.wait)
		}
	}
}

// An answer with no Agents-Refusal is a plain failed dial, retried at the usual rate.
func TestDialWithoutARefusalIsNotOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "There is no control plane here.", http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), srv.URL, "")
	var r *AddressRefusal
	if err == nil || errors.As(err, &r) {
		t.Fatalf("got %v, want a failed dial with no refusal", err)
	}
	if got := (&Uplink{}).refusedWait(err); got != 0 {
		t.Fatalf("waits %s, want 0", got)
	}
}

// A refusal's line is cut to refusalLineMost and kept on one line.
func TestAddressRefusalLineIsBounded(t *testing.T) {
	long := make([]byte, 4*refusalLineMost)
	for i := range long {
		long[i] = 'a'
	}
	long[3] = '\n'
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Agents-Refusal", Gone)
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write(long)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), srv.URL, "")
	var r *AddressRefusal
	if !errors.As(err, &r) || len(r.Line) > refusalLineMost {
		t.Fatalf("got %v", err)
	}
}
