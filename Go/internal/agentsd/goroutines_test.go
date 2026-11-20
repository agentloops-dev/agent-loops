package agentsd

import (
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goroutinesNotCounted are the goroutines agentsd starts with a bare `go` or safe.Go, not
// d.goJob, and why each one may (#6130). Each is "file func", and how many that function
// starts. Shutdown does not wait for them, so startWith's cleanup cannot name one that
// outlives it: a goroutine that lives as long as the daemon, or ends only when something
// outside it does, goes through d.goJob (or d.jobFunc, d.goLoop). One that its own function
// waits for, or that ends when a value it alone owns closes, can stay here.
var goroutinesNotCounted = map[string]int{
	// Joined: the function waits for them (a WaitGroup, a channel it reads, or a kill at a limit).
	"connectorsetup.go (*Daemon).readSetup":     1, // it reads one answer from each
	"docs.go (*Daemon).askConnectors":           1,
	"docsmap.go (*Daemon).searchDocsServers":    1,
	"handoffwindow.go frontApp":                 2, // agents-drive's answer, or a kill at windowWaitsLongest
	"mcpclient.go (*stdioMCP).end":              1,
	"mcpeventclient.go (*mcpEventConns).endAll": 1,
	"mcpeventclient.go (*mcpEventConns).drop":   1, // endAll waits for it, by the pool's ending (#6171)
	"runtimes.go (*Daemon).runtimeStatuses":     1,
	"screencalls.go (*driveProcess).call":       1, // a write to agents-drive; stop closes its stdin
	"screencalls.go (*driveProcess).stop":       1,
	"watch_darwin.go (*folderWatch).stop":       1,
	// They end when a value they alone own closes, and Shutdown closes it (stopFolderWatches,
	// stopScreenDrives, stopMCPEvents, the socket's and the page's close).
	"pagerelay.go newPageOut":                 1,
	"pagerelay.go (*pageChannel).take":        1,
	"rpc.go newSockConn":                      1,
	"screencalls.go (*Daemon).screenDriveFor": 1,
	"watch_darwin.go startFolderWatch":        2,
	"watch_darwin.go (*folderWatch).gather":   1, // stop, off the reader it would wait for
	"watch_linux.go startFolderWatch":         1,
	"watch_linux.go (*folderWatch).read":      1, // stop, off the reader it would wait for
	// They end when a process or a call outside the daemon does, at a limit of their own.
	"languageservers.go startLanguageServer":       2, // the server's output and its exit
	"languageservers.go (*languageServers).server": 1, // the process's pool, not one daemon's
	"mac_darwin.go macOpen":                        1, // /usr/bin/open's exit
	"mcpclient.go startStdioMCP":                   1, // the server's output
	"mcpsignin.go listenForCallback":               1, // ends when the sign-in closes its server
	"mcpsignin.go probeSignIn":                     1, // one DELETE, in mcpSignInHTTP's timeout
	// Not a job: it can call d.Quit (u.restart on Linux), and Shutdown would wait for itself.
	"selfupdate.go (*selfUpdater).Named": 1,
	// Ends when the codespace's lease does (#4637); awake has no Daemon to count it with.
	"keepawake.go (*awake).keepAwake": 1,
}

// TestEveryDaemonGoroutineIsCounted fails on a goroutine that agentsd starts outside d.goJob
// and that goroutinesNotCounted does not list (#6130): Shutdown would not wait for it, and it
// would fail some other test, days later, as it wrote into a root that TempDir removes.
func TestEveryDaemonGoroutineIsCounted(t *testing.T) {
	t.Parallel()
	found := map[string]int{}
	var sites []string
	fset := gotoken.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "jobs.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			key := name + " " + declName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				if !startsGoroutine(n) {
					return true
				}
				found[key]++
				sites = append(sites, fmt.Sprintf("%s (%s)", key, fset.Position(n.Pos())))
				return true
			})
		}
	}
	for key, n := range found {
		if n > goroutinesNotCounted[key] {
			var at []string
			for _, s := range sites {
				if strings.HasPrefix(s, key+" (") {
					at = append(at, s)
				}
			}
			slices.Sort(at)
			t.Errorf("%s starts %d goroutines outside d.goJob, and goroutinesNotCounted allows %d: start it with d.goJob (d.jobFunc for a timer), so Shutdown waits for it, or list it there with why it may not:\n\t%s",
				key, n, goroutinesNotCounted[key], strings.Join(at, "\n\t"))
		}
	}
	for key, n := range goroutinesNotCounted {
		if found[key] < n {
			t.Errorf("goroutinesNotCounted allows %d for %q, which starts %d: lower it", n, key, found[key])
		}
	}
}

// startsGoroutine is whether n is a `go` statement or a call of safe.Go.
func startsGoroutine(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.GoStmt:
		return true
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "safe" && sel.Sel.Name == "Go"
	}
	return false
}

// declName is the name of a function, or of the first variable a declaration makes: a
// `var f = func() {…}` starts goroutines as a function does.
func declName(decl ast.Decl) string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return funcName(decl)
	case *ast.GenDecl:
		for _, spec := range decl.Specs {
			if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) > 0 {
				return v.Names[0].Name
			}
		}
	}
	return "?"
}

// funcName is fn's name, with its receiver's type for a method: "(*Daemon).watch".
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	star := ""
	if s, ok := recv.(*ast.StarExpr); ok {
		star, recv = "*", s.X
	}
	if g, ok := recv.(*ast.IndexExpr); ok {
		recv = g.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return "(" + star + id.Name + ")." + fn.Name.Name
	}
	return fn.Name.Name
}
