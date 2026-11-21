package main

import (
	"path"
	"slices"
	"sort"
	"strings"
)

// The checks `make check` runs, as Makefile targets in the order its `check`
// rule lists them. TestChecksMatchMakefile keeps the two lists equal.
const (
	checkTest        = "test"
	checkVet         = "vet"
	checkStaticcheck = "staticcheck"
	checkTidy        = "tidy-check"
	checkWebTest     = "web-test"
	checkWeb         = "web"
)

var allChecks = []string{checkTest, checkVet, checkStaticcheck, checkTidy, checkWebTest, checkWeb}

// goChecks take the Go packages to check in GO_PKGS.
var goChecks = []string{checkTest, checkVet, checkStaticcheck}

// archguardDir holds the architecture tests. They read every package's
// source, so any Go change runs them.
const archguardDir = "internal/archguard"

// rule says what a change under one path needs. A file takes the first rule
// whose path matches it.
type rule struct {
	// path is a file, or a directory when it ends in "/", relative to the
	// repository root.
	path string
	// checks run in full when the path changes; a Go check here runs on every
	// package.
	checks []string
	// pkgs are package directories whose tests read the path although it lies
	// outside them; they and their importers are checked like changed Go code.
	pkgs []string
	// why is shown in the plan for a rule that selects nothing.
	why string
}

// rules is the one path-to-check table: `make check BASE=<ref>` and CI both
// plan from it. A Go file always selects its own package. Any other file that
// no rule matches selects the package whose directory holds it (its testdata
// or embedded files), or nothing outside every package.
var rules = []rule{
	// What runs and plans the checks: a mistake here could hide a failure.
	{path: "Makefile", checks: allChecks},
	{path: "scripts/checkplan/", checks: allChecks},
	{path: ".github/workflows/ci.yml", checks: allChecks},

	// A module change can move any dependency of any package.
	{path: "go.mod", checks: []string{checkTidy, checkTest, checkVet, checkStaticcheck}},
	{path: "go.sum", checks: []string{checkTidy, checkTest, checkVet, checkStaticcheck}},
	{path: "staticcheck.conf", checks: []string{checkStaticcheck}},

	// The React Console. Its Go files (web/*.go) still select package web.
	{path: "web/", checks: []string{checkWebTest, checkWeb}},

	// Files outside any package that Go tests read.
	{path: "install.sh", pkgs: []string{"internal/postgressetup"}},
	{path: "sdk/python/", pkgs: []string{"platform/feishu"}},

	// Not part of `make check`.
	{path: "docs/", why: "docs (no link checker in this repository)"},
	{path: "desktop/", why: "desktop shell (built by make desktop, not make check)"},
	{path: "macos-menubar/", why: "menu bar app (built by make menubar, not make check)"},
	{path: "android/", why: "Android app (not part of make check)"},
	{path: "ios/", why: "iOS app (not part of make check)"},
	{path: "sdk/", why: "SDKs (checked by the SDK workflow, not make check)"},
}

func matchRule(file string) (rule, bool) {
	for _, r := range rules {
		if r.path == file || strings.HasSuffix(r.path, "/") && strings.HasPrefix(file, r.path) {
			return r, true
		}
	}
	return rule{}, false
}

// goPackage is one package of the module, from `go list ./...`.
type goPackage struct {
	Dir         string   // relative to the repository root, slash-separated; "." for the root
	ImportPath  string   //
	Imports     []string // production imports
	TestImports []string // imports of its _test.go files, internal and external
}

// graph is the module's packages.
type graph struct {
	Module string
	Pkgs   []goPackage
}

func (g *graph) importPath(dir string) string {
	if dir == "." {
		return g.Module
	}
	return g.Module + "/" + dir
}

// enclosingPackage is the package whose directory is nearest above file, or "".
func (g *graph) enclosingPackage(file string) string {
	dirs := make(map[string]bool, len(g.Pkgs))
	for _, p := range g.Pkgs {
		dirs[p.Dir] = true
	}
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if dirs[dir] {
			return dir
		}
		if dir == "." || dir == "/" {
			return ""
		}
	}
}

// affected lists the directories of the packages a change to the given
// import paths can break: those packages, the packages that import them
// directly or not, and the packages whose tests import any of those. An
// import path need not exist any more; its former importers are found all the
// same, since `go list -e` still reports the import.
func (g *graph) affected(changed map[string]bool) []string {
	importers := map[string][]string{}
	for _, p := range g.Pkgs {
		for _, imp := range p.Imports {
			importers[imp] = append(importers[imp], p.ImportPath)
		}
	}
	reach := map[string]bool{}
	var queue []string
	for ip := range changed {
		queue = append(queue, ip)
	}
	for len(queue) > 0 {
		ip := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if reach[ip] {
			continue
		}
		reach[ip] = true
		queue = append(queue, importers[ip]...)
	}
	var out []string
	for _, p := range g.Pkgs {
		if reach[p.ImportPath] || slices.ContainsFunc(p.TestImports, func(ip string) bool { return reach[ip] }) {
			out = append(out, p.Dir)
		}
	}
	sort.Strings(out)
	return out
}

// step is one check to run.
type step struct {
	Target string
	// Pkgs are the package directories a Go check runs on; nil runs it on
	// every package.
	Pkgs []string
}

// fileNote says what one changed file selected, for the plan.
type fileNote struct {
	File string
	Note string
}

// plan is what a change set needs.
type plan struct {
	Steps   []step
	Files   []fileNote
	Changed []string // package directories changed directly
}

// isGoSource reports whether file is a Go file the go tool builds: not under
// testdata or a directory it ignores.
func isGoSource(file string) bool {
	if !strings.HasSuffix(file, ".go") {
		return false
	}
	for _, elem := range strings.Split(path.Dir(file), "/") {
		if elem == "testdata" || strings.HasPrefix(elem, "_") || strings.HasPrefix(elem, ".") && elem != "." {
			return false
		}
	}
	return true
}

// makePlan maps changed files, relative to the repository root, to checks.
// A nil graph (the packages could not be listed) runs every Go check on
// every package whenever anything selects Go.
func makePlan(files []string, g *graph) plan {
	var p plan
	full := map[string]bool{} // checks to run in full
	changed := map[string]bool{}
	goFile := false
	for _, f := range files {
		var notes []string
		r, matched := matchRule(f)
		for _, c := range r.checks {
			full[c] = true
		}
		if matched && len(r.checks) > 0 {
			if len(r.checks) == len(allChecks) {
				notes = append(notes, "every check")
			} else {
				notes = append(notes, describeFull(r.checks))
			}
		}
		var dirs []string
		dirs = append(dirs, r.pkgs...)
		switch {
		case isGoSource(f):
			goFile = true
			dirs = append(dirs, path.Dir(f))
		case !matched && g != nil:
			if d := g.enclosingPackage(f); d != "" {
				dirs = append(dirs, d)
			}
		}
		for _, d := range dirs {
			if g == nil {
				full[checkTest], full[checkVet], full[checkStaticcheck] = true, true, true
				continue
			}
			changed[g.importPath(d)] = true
			p.Changed = append(p.Changed, d)
			notes = append(notes, "Go package "+pkgArg(d))
		}
		if len(notes) == 0 {
			why := r.why
			if why == "" {
				why = "not part of make check"
			}
			notes = append(notes, "no check: "+why)
		}
		p.Files = append(p.Files, fileNote{File: f, Note: strings.Join(notes, "; ")})
	}
	slices.Sort(p.Changed)
	p.Changed = slices.Compact(p.Changed)

	var affected []string
	if g != nil {
		affected = g.affected(changed)
	}
	testPkgs := affected
	if goFile && !slices.Contains(testPkgs, archguardDir) {
		testPkgs = append(slices.Clone(testPkgs), archguardDir)
		sort.Strings(testPkgs)
	}
	for _, c := range allChecks {
		switch {
		case full[c]:
			p.Steps = append(p.Steps, step{Target: c})
		case c == checkTest && len(testPkgs) > 0:
			p.Steps = append(p.Steps, step{Target: c, Pkgs: testPkgs})
		case slices.Contains(goChecks, c) && len(affected) > 0:
			p.Steps = append(p.Steps, step{Target: c, Pkgs: affected})
		}
	}
	return p
}

func describeFull(checks []string) string {
	s := strings.Join(checks, ", ")
	if slices.ContainsFunc(checks, func(c string) bool { return slices.Contains(goChecks, c) }) {
		s += " on every Go package"
	}
	return s
}

// pkgArg is the go command argument for a package directory.
func pkgArg(dir string) string {
	if dir == "." {
		return "."
	}
	return "./" + dir
}
