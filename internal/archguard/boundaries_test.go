package archguard

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The layering docs/architecture/hub-and-super-agents.md §8 aims for:
// core/ holds domain types and interfaces, the link and relay packages carry
// bytes between machines, the services sit above them, and server/ is a thin
// HTTP transport that only composition roots mount. Today's graph already
// keeps most of that; the rules below stop it from getting worse, and the
// allowlist names what still breaks them.
//
// Every rule reads production imports only (go list without tests). Paths are
// relative to the module ("core", "cmd/amux"); a path outside the module is
// written in full ("net/http").

// boundaryRule is one rule of the layering.
type boundaryRule struct {
	name string
	// governs says which packages the rule applies to.
	governs func(rel string) bool
	// transitive checks every dependency, not only direct imports.
	transitive bool
	// forbidden says whether the governed package may not depend on dep:
	// a path relative to the module when inModule, a full path otherwise.
	forbidden func(dep string, inModule bool) bool
	// instead tells the author what to do rather than add the dependency.
	instead string
}

// Service and transport packages: everything a domain or link package must
// not reach. Composition roots (cmd/..., bootstrap) wire them together.
var (
	applicationPackages = []string{
		"server", "bootstrap", "cmd",
		"superagent", "orchestration", "mailbox", "push",
		"framework", "agent", "platform",
	}
	// linkPackages carry bytes between machines and the hub: the node link,
	// the relay in front of two hubs and standby replication.
	linkPackages = []string{"link", "hubrelay", "hubha"}
	// compositionRoots may import server: they build the daemon.
	// desktop/ (build tag "desktop") is one too, but go list skips it by
	// default, so no rule ever sees it.
	compositionRoots = []string{"cmd", "bootstrap", "desktop"}
	// transportImports are packages that put a network, process or database
	// in the importer's hands.
	transportImports = []string{
		"net", "net/http", "os/exec", "database/sql",
		"github.com/jackc/pgx", "modernc.org/sqlite",
		"github.com/gorilla/websocket", "github.com/hashicorp/yamux",
	}
)

func underAny(rel string, pkgs []string) bool {
	return slices.ContainsFunc(pkgs, func(p string) bool { return under(rel, p) })
}

var boundaryRules = []boundaryRule{
	{
		// core-leaf: core/ imports no other package of this module. It is the
		// bottom of the graph; every other package may import it.
		name:      "core-leaf",
		governs:   func(rel string) bool { return under(rel, "core") },
		forbidden: func(dep string, inModule bool) bool { return inModule && !under(dep, "core") },
		instead:   "core/ holds domain types and interfaces only: define the interface in core and implement it in the other package",
	},
	{
		// core-no-transport: core/ opens no connection, process or database.
		// The doc admits core still runs the channel runtime; the allowlist
		// names the imports that carries.
		name:      "core-no-transport",
		governs:   func(rel string) bool { return under(rel, "core") },
		forbidden: func(dep string, inModule bool) bool { return !inModule && underAny(dep, transportImports) },
		instead:   "core/ is domain types only: put the code that talks to the network, a process or a database in a runtime or service package and give core an interface",
	},
	{
		// link-below-services: link, hubrelay and hubha depend, even
		// indirectly, on no service, transport or composition root. The hub
		// and the standby must be able to use them without the node's
		// application; a service reaches them through an interface it owns
		// (superagent.HubRelay, superagent.HostDialer).
		name:       "link-below-services",
		governs:    func(rel string) bool { return underAny(rel, linkPackages) },
		transitive: true,
		forbidden:  func(dep string, inModule bool) bool { return inModule && underAny(dep, applicationPackages) },
		instead:    "the link packages only carry bytes: declare an interface in the link package (or in core) and let the service implement it",
	},
	{
		// only-roots-import-server: nothing but a composition root (cmd/...,
		// bootstrap) or server/ itself depends on server/, even indirectly.
		// server/ is the HTTP transport; services must not need it, so the hub
		// and the node can mount the same services.
		name:       "only-roots-import-server",
		governs:    func(rel string) bool { return !underAny(rel, compositionRoots) && !under(rel, "server") },
		transitive: true,
		forbidden:  func(dep string, inModule bool) bool { return inModule && under(dep, "server") },
		instead:    "move what you need out of server/ into a service package that both import",
	},
}

// TestPackageBoundaries checks every rule against the import graph. A
// violation passes only with a line "<rule> <package> <dependency>" in
// testdata/boundary_allowed.txt, and a line that matches nothing fails, so
// the list can only shrink.
func TestPackageBoundaries(t *testing.T) {
	pkgs := moduleGraph(t)
	mod := modulePath(t)
	violations := boundaryViolations(pkgs, mod, boundaryRules)
	checkAllowlist(t, "boundary_allowed.txt", violations)
}

// violation is one broken rule, keyed by its allowlist line.
type violation struct {
	key     string // "<rule> <package> <dependency>"
	message string
}

func boundaryViolations(pkgs []goPackage, mod string, rules []boundaryRule) []violation {
	var out []violation
	for _, r := range rules {
		for _, p := range pkgs {
			if !r.governs(p.Rel) {
				continue
			}
			deps := p.Imports
			if r.transitive {
				deps = p.Deps
			}
			for _, full := range deps {
				dep := relPath(mod, full)
				inModule := full == mod || strings.HasPrefix(full, mod+"/")
				if !r.forbidden(dep, inModule) {
					continue
				}
				out = append(out, violation{
					key: r.name + " " + p.Rel + " " + dep,
					message: fmt.Sprintf("%s depends on %s, which rule %s forbids: %s; %s",
						p.Rel, dep, r.name, r.instead, guardDoc),
				})
			}
		}
	}
	return out
}

// checkAllowlist fails on every violation the list does not name and on
// every line that names no violation.
func checkAllowlist(t *testing.T, list string, violations []violation) {
	t.Helper()
	allowed := readList(t, list)
	used := map[string]bool{}
	for _, v := range violations {
		if slices.ContainsFunc(allowed, func(l listLine) bool { return l.Text == v.key }) {
			used[v.key] = true
			continue
		}
		t.Error(v.message)
	}
	for _, l := range allowed {
		if !used[l.Text] {
			t.Errorf("testdata/%s:%d allows %q, which no longer happens: delete the line (the list only shrinks); %s",
				list, l.Line, l.Text, guardDoc)
		}
	}
}
