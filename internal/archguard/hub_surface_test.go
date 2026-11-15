package archguard

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
)

// The hub is a meeting point: it pairs devices, relays between them and keeps
// a short list of hub data (docs/architecture/hub-and-super-agents.md §4). These
// tests pin what crosses it, so that a new route up the node link, a new call
// through the worker relay or a new replicated table is a line a reviewer
// reads in testdata/ rather than something that slips in with a handler.

// uplinkMarker picks out the hub's side of the node link: what a linked
// machine sends up its link lands on paths under /uplink/.
const uplinkMarker = "/uplink/"

// TestUplinkRoutes pins every route the hub serves up the node link in
// testdata/uplink_routes.txt. It reads the module's source, so routes in
// unexported constants and in any package count:
//
//   - "<pattern>" for a Handle or HandleFunc call whose pattern contains
//     /uplink/ (link/report.go, hubha/feed.go, server/mailbox.go,
//     server/worker_relay.go today);
//   - "prefix <path>" for a strings.HasPrefix(….URL.Path, path) dispatch to
//     a path under /uplink/ (server/hubstandby.go hands /uplink/v1/hub/ to
//     the standby feed);
//   - "path <path>" for a ….URL.Path == path comparison under /uplink/.
//
// An uplink pattern the guard cannot evaluate fully (not only literals, `+`
// and string constants) fails, so a route cannot hide behind a variable. A
// pattern that is nothing but a variable is not seen at all; review catches
// that.
func TestUplinkRoutes(t *testing.T) {
	idx := loadSources(t)
	routes := map[string]string{}
	for importPath, pkg := range idx.packages {
		for _, file := range pkg.Files {
			ast.Inspect(file.AST, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					collectHandle(t, idx, importPath, file, n, routes)
					collectPrefix(idx, importPath, file, n, routes)
				case *ast.BinaryExpr:
					if n.Op != token.EQL {
						return true
					}
					for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
						if isURLPath(pair[0]) {
							if p, ok := idx.eval(pair[1], importPath, file); ok && strings.Contains(p, uplinkMarker) {
								routes["path "+p] = idx.pos(n)
							}
						}
					}
				}
				return true
			})
		}
	}
	if len(routes) == 0 {
		t.Fatal("found no uplink route at all: the guard no longer recognizes how routes are registered; update hub_surface_test.go")
	}
	compareSets(t, "uplink_routes.txt", readList(t, "uplink_routes.txt"), routes, "uplink route",
		"only the hub's own communication belongs up the link (pairing, reports, relays, the standby feed, mail); "+
			"if this is that, add the line in the same change")
}

// collectHandle records mux.Handle / mux.HandleFunc patterns under /uplink/.
func collectHandle(t *testing.T, idx *sourceIndex, importPath string, file *sourceFile, call *ast.CallExpr, routes map[string]string) {
	t.Helper()
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") || len(call.Args) != 2 {
		return
	}
	pattern, ok := idx.eval(call.Args[0], importPath, file)
	if !ok {
		// A pattern partly built at run time (server/remote.go ranges over
		// methods) is fine unless what can be read of it lies under /uplink/:
		// such a route could not be pinned.
		if partial := idx.evalPartial(call.Args[0], importPath, file); strings.Contains(partial, uplinkMarker) {
			t.Errorf("%s registers the uplink route %q, which the guard cannot evaluate fully: build the pattern from string literals and constants; %s",
				idx.pos(call), partial, guardDoc)
		}
		return
	}
	if strings.Contains(pattern, uplinkMarker) {
		routes[pattern] = idx.pos(call)
	}
}

// collectPrefix records strings.HasPrefix(….URL.Path, "/uplink/…") dispatches.
func collectPrefix(idx *sourceIndex, importPath string, file *sourceFile, call *ast.CallExpr, routes map[string]string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "HasPrefix" || len(call.Args) != 2 || !isURLPath(call.Args[0]) {
		return
	}
	if x, ok := sel.X.(*ast.Ident); !ok || file.imports[x.Name] != "strings" {
		return
	}
	if p, ok := idx.eval(call.Args[1], importPath, file); ok && strings.Contains(p, uplinkMarker) {
		routes["prefix "+p] = idx.pos(call)
	}
}

// isURLPath matches an expression ending in .URL.Path.
func isURLPath(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Path" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "URL"
}

// TestWorkerRelayCalls pins the worker API calls the hub's worker relay lets
// through (superagent.RelayAllowed) in testdata/relay_allowed.txt. The list
// lives in a switch, so the guard reads the switch from source, and then asks
// the real function about every pinned line.
func TestWorkerRelayCalls(t *testing.T) {
	idx := loadSources(t)
	pkgPath := modulePath(t) + "/superagent"
	pkg := idx.packages[pkgPath]
	if pkg == nil {
		t.Fatalf("no source for %s", pkgPath)
	}
	var fn *ast.FuncDecl
	var fnFile *sourceFile
	for _, file := range pkg.Files {
		for _, decl := range file.AST.Decls {
			if d, ok := decl.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == "RelayAllowed" {
				fn, fnFile = d, file
			}
		}
	}
	if fn == nil {
		t.Fatal("superagent.RelayAllowed is gone: update hub_surface_test.go to read the relay's list from where it lives now")
	}
	calls := map[string]string{}
	shapeOK := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok || !returnsTrue(clause.Body) {
			return true
		}
		shapeOK = true
		for _, e := range clause.List {
			call, ok := idx.eval(e, pkgPath, fnFile)
			if !ok {
				t.Errorf("%s: the guard cannot evaluate this RelayAllowed case: use string literals and constants; %s", idx.pos(e), guardDoc)
				continue
			}
			calls[call] = idx.pos(e)
		}
		return true
	})
	if !shapeOK {
		t.Fatalf("superagent.RelayAllowed no longer is a switch whose cases return true: update hub_surface_test.go to read its list")
	}
	listed := readList(t, "relay_allowed.txt")
	compareSets(t, "relay_allowed.txt", listed, calls, "relayed worker call",
		"the relay carries only the worker API between one owner's machines; if this call is part of it, add the line in the same change")
	for _, l := range listed {
		method, path, ok := strings.Cut(l.Text, " ")
		if !ok {
			t.Errorf("testdata/relay_allowed.txt:%d: want \"METHOD /path\", got %q", l.Line, l.Text)
			continue
		}
		if !superagent.RelayAllowed(method, path) {
			t.Errorf("testdata/relay_allowed.txt:%d lists %q, but superagent.RelayAllowed refuses it", l.Line, l.Text)
		}
	}
}

func returnsTrue(body []ast.Stmt) bool {
	if len(body) == 0 {
		return false
	}
	ret, ok := body[len(body)-1].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	id, ok := ret.Results[0].(*ast.Ident)
	return ok && id.Name == "true"
}

// TestHubTables pins the tables a standby hub copies (store.HubTables) in
// testdata/hub_tables.txt. Hub data is everything a failover carries to the
// other machine, so a table joins it only on purpose.
func TestHubTables(t *testing.T) {
	inCode := map[string]string{}
	for _, table := range store.HubTables {
		if _, dup := inCode[table]; dup {
			t.Errorf("store.HubTables lists %q twice", table)
		}
		inCode[table] = "store.HubTables"
	}
	compareSets(t, "hub_tables.txt", readList(t, "hub_tables.txt"), inCode, "hub table",
		"a hub table is copied to the standby and fenced on a stale primary; add the line in the same change once that is intended "+
			"(a table a node also writes for itself, like the mailbox's, cannot be one)")
}
