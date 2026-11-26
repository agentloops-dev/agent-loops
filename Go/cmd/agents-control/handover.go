package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// ownCopy is `self`: with --home, the address and pin `serve --home` gives itself, so a
// copy started empty to receive is reachable too (T127); otherwise what its settings say.
func ownCopy() (string, string) {
	if home() != "" {
		port := 8791
		if p, err := strconv.Atoi(value("--port")); err == nil {
			port = p
		}
		text := os.Getenv("AGENTS_CONTROL_URL")
		if text == "" {
			text = fmt.Sprintf("https://%s:%d", controlplane.LocalHostName(), port)
		}
		u, err := url.Parse(text)
		if err != nil || u.Hostname() == "" {
			fail("%s is not an address", text)
		}
		_, pin, err := controlplane.SelfSigned(filepath.Join(home(), "tls"), u.Hostname())
		if err != nil {
			fail("this Mac's certificate: %v", err)
		}
		return text, pin
	}
	r := records.New(openStore(""))
	if err := r.Load(context.Background()); err != nil {
		fail("%v", err)
	}
	settings := r.Settings()
	if settings == nil || settings.URL == nil {
		fail("this store has no control plane yet")
	}
	pin := ""
	if settings.Pin != nil {
		pin = *settings.Pin
	}
	return *settings.URL, pin
}

// handover is a handover of the running control plane to a copy elsewhere (058, R16), one
// step at a time, with the control plane's key: Agents Host's Move to Another Machine… and
// Run It Here Again…, or by hand. --at is the copy the step is for.
func handover() {
	step := ""
	if len(args) > 1 {
		step = args[1]
	}
	if step == "" || strings.HasPrefix(step, "-") {
		fail("%s", handoverUsage)
	}
	h := &handoverLinks{}
	switch step {
	case "status", "freeze", "unfreeze", "withdraw", "take", "stop":
		showHandoverStatus(h.link("--at", "--pin").Call("handover/"+step, nil))
	case "announce":
		showHandoverStatus(h.link("--at", "--pin").Call("handover/announce", map[string]any{"endpoint": handoverEndpoints()[0], "force": has("--force")}))
	case "forward":
		// 30 days at most, the copy's own rule; --days for fewer.
		params := map[string]any{"endpoints": handoverEndpoints()}
		if days, err := strconv.ParseFloat(value("--days"), 64); err == nil {
			params["until"] = wire.DateOf(time.Now().Add(time.Duration(days * 24 * float64(time.Hour))))
		}
		showHandoverStatus(h.link("--at", "--pin").Call("handover/forward", params))
	case "shares":
		h.shares()
	case "copy":
		h.copy()
	default:
		fail("%s", handoverUsage)
	}
}

// handoverLinks dials the copies a handover step names, with the control plane's key,
// read from the store the first time a step needs it.
type handoverLinks struct {
	key []byte
}

func (h *handoverLinks) link(flag, pinFlag string) *controlplane.HandoverLink {
	text, pin := value(flag), value(pinFlag)
	if text == "" {
		fail("say %s https://… (or self) for the copy", flag)
	}
	if text == "self" {
		text, pin = ownCopy()
	}
	if h.key == nil {
		h.key = privateKey(openStore(""))
	}
	l, err := controlplane.DialHandover(context.Background(), text, pin, h.key)
	if err != nil {
		fail("couldn't reach %s as this control plane: %v", text, err)
	}
	return l
}

// shares says whether two copies read one store (a bucket, R16 3): a mark left at one is
// found at the other.
func (h *handoverLinks) shares() {
	at, with := h.link("--at", "--pin"), h.link("--with", "--with-pin")
	nonce := make([]byte, 16)
	rand.Read(nonce)
	mark := hex.EncodeToString(nonce)
	if _, err := at.Call("handover/mark", map[string]any{"nonce": mark, "leave": true}); err != nil {
		fail("%v", err)
	}
	reply, err := with.Call("handover/mark", map[string]any{"nonce": mark})
	var found struct{ Found bool }
	if err != nil || json.Unmarshal(reply, &found) != nil || !found.Found {
		fail("not the same store: %s doesn't see what %s wrote", value("--with"), value("--at"))
	}
	fmt.Println("the same store")
}

// copy copies every record from --from to --to.
func (h *handoverLinks) copy() {
	from := h.end("--from", "--from-pin")
	to := h.end("--to", "--to-pin")
	reportCopy(controlplane.CopyStore(context.Background(), from, to))
}

// end is either end of a copy: a copy (https://… or self, over its handover session) or a
// store this machine opens (file://…, s3://…).
func (h *handoverLinks) end(flag, pinFlag string) store.Store {
	text := value(flag)
	if text == "" {
		fail("say %s", flag)
	}
	if text == "self" || strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "http://") {
		return controlplane.PeerStore{Link: h.link(flag, pinFlag)}
	}
	return openStore(text)
}

// handoverEndpoints is the one endpoint that --endpoint and --endpoint-pin give.
func handoverEndpoints() []records.Endpoint {
	text := value("--endpoint")
	if text == "" {
		fail("say --endpoint https://… (or self) [--endpoint-pin PIN]")
	}
	pin := value("--endpoint-pin")
	if text == "self" {
		text, pin = ownCopy()
	}
	e := records.Endpoint{URL: text}
	if pin != "" {
		e.Pin = &pin
	}
	return []records.Endpoint{e}
}

// showHandoverStatus prints a handover step's reply: a status as lines of tab-separated
// fields, or the raw reply with --json or when it is no status.
func showHandoverStatus(result json.RawMessage, err error) {
	if err != nil {
		fail("%v", err)
	}
	var status wire.HandoverStatus
	if json.Unmarshal(result, &status) != nil || status.Phase == "" || has("--json") {
		fmt.Println(string(result))
		return
	}
	fmt.Printf("phase\t%s\n", status.Phase)
	if status.ForwardingUntil != nil {
		fmt.Printf("until\t%s\n", status.ForwardingUntil.Time().UTC().Format(time.RFC3339))
	}
	recordsText := "none"
	if status.HasRecords {
		recordsText = "yes"
	}
	fmt.Printf("records\t%s\n", recordsText)
	epoch := "-"
	if status.Epoch != nil {
		epoch = strconv.FormatInt(*status.Epoch, 10)
	}
	fmt.Printf("epoch\t%s\n", epoch)
	for _, e := range status.Endpoints {
		pin := "-"
		if e.Pin != nil {
			pin = *e.Pin
		}
		fmt.Printf("endpoint\t%s\t%s\n", e.URL, pin)
	}
	for _, m := range status.Members {
		known, online := "-", "-"
		if m.KnownEpoch != nil {
			known = strconv.FormatInt(*m.KnownEpoch, 10)
		}
		if m.Online != nil && *m.Online {
			online = "online"
		}
		fmt.Printf("member\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.Name, m.Kind, known, online)
	}
}

// copyStore is Switch Store… (T058b): with no copy serving, every record into an empty
// store.
func copyStore() {
	from, to := value("--from"), value("--to")
	if from == "" || to == "" {
		fail("say --from URL --to URL")
	}
	reportCopy(controlplane.CopyStore(context.Background(), openStore(from), openStore(to)))
}

func reportCopy(copied, skipped int, err error) {
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("copied %d records (left out %d leases, copies and sealed keys, which each copy makes its own)\n", copied, skipped)
}

const handoverUsage = `usage: agents-control handover status|freeze|unfreeze|withdraw|take|stop --at URL [--pin PIN] [--json]
       agents-control handover shares --at URL [--pin PIN] --with URL [--with-pin PIN]
       agents-control handover announce|forward --at URL [--pin PIN] --endpoint URL [--endpoint-pin PIN] [--days N] [--force]
       agents-control handover copy --from URL|STORE [--from-pin PIN] --to URL|STORE [--to-pin PIN]
(URL may be self: this copy, at the address and pin serve --home gives itself, or its settings say)`
