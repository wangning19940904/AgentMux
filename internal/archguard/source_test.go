package archguard

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// guardDoc ends every failure message so a reader knows where the rules are.
const guardDoc = "see docs/architecture/guards.md"

// moduleRoot is the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// modulePath is the module's import path, read from go.mod.
func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod names no module")
	return ""
}

// listLine is one entry of a testdata list.
type listLine struct {
	Text string // the entry, with runs of spaces collapsed and the comment removed
	Line int
}

// readList reads testdata/<name>: one entry per line, `#` starts a comment,
// blank lines are skipped. A duplicate entry fails the test.
func readList(t *testing.T, name string) []listLine {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []listLine
	seen := map[string]int{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		text := sc.Text()
		if i := strings.Index(text, "#"); i >= 0 {
			text = text[:i]
		}
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		if first, dup := seen[text]; dup {
			t.Errorf("testdata/%s:%d repeats line %d (%q): delete one", name, n, first, text)
			continue
		}
		seen[text] = n
		out = append(out, listLine{Text: text, Line: n})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// compareSets reports what is in code but not listed, and what is listed but
// not in code. what names the thing ("uplink route"), fix says how to make a
// legitimate new entry pass.
func compareSets(t *testing.T, list string, listed []listLine, inCode map[string]string, what, fix string) {
	t.Helper()
	pinned := map[string]bool{}
	for _, l := range listed {
		pinned[l.Text] = true
		if _, ok := inCode[l.Text]; !ok {
			t.Errorf("testdata/%s:%d lists %s %q, which the code no longer has: delete the line; %s",
				list, l.Line, what, l.Text, guardDoc)
		}
	}
	for _, entry := range slices.Sorted(mapKeys(inCode)) {
		if !pinned[entry] {
			t.Errorf("new %s %q (%s) is not in testdata/%s: %s; %s", what, entry, inCode[entry], list, fix, guardDoc)
		}
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// goPackage is one package of the module, as `go list` reports it.
type goPackage struct {
	Rel     string   // import path relative to the module ("core", "cmd/amux")
	Imports []string // direct imports of non-test files, full paths
	Deps    []string // transitive dependencies of non-test files, full paths
}

var (
	graphOnce sync.Once
	graph     []goPackage
	graphErr  error
)

// moduleGraph lists every package of the module with its production imports
// and dependencies. Test files are left out on purpose: a test may import
// whatever it needs to check its package.
func moduleGraph(t *testing.T) []goPackage {
	t.Helper()
	root, mod := moduleRoot(t), modulePath(t)
	// go test caches a passing result keyed on the files the test process
	// opens; what the go list child reads is invisible to it. Reading every
	// source file here makes an edit anywhere in the module invalidate the
	// cached result.
	loadSources(t)
	graphOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			graphErr = err
			return
		}
		cmd := exec.Command(goBin, "list", "-f",
			"{{.ImportPath}}\t{{join .Imports \" \"}}\t{{join .Deps \" \"}}", "./...")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			graphErr = fmt.Errorf("go list: %v: %s", err, stderr.String())
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) != 3 {
				graphErr = fmt.Errorf("go list printed %q", line)
				return
			}
			graph = append(graph, goPackage{
				Rel:     relPath(mod, fields[0]),
				Imports: strings.Fields(fields[1]),
				Deps:    strings.Fields(fields[2]),
			})
		}
	})
	if graphErr != nil {
		t.Fatal(graphErr)
	}
	return graph
}

// relPath turns a full import path into one relative to the module; a path
// outside the module comes back unchanged.
func relPath(mod, importPath string) string {
	if importPath == mod {
		return "."
	}
	if rel, ok := strings.CutPrefix(importPath, mod+"/"); ok {
		return rel
	}
	return importPath
}

// under says whether rel is pkg or one of its subpackages.
func under(rel, pkg string) bool { return rel == pkg || strings.HasPrefix(rel, pkg+"/") }

// sourceIndex is the module's non-test Go source, parsed, with every
// package-level string constant it can evaluate. The hub-surface guards read
// lists that are not exported (route patterns, a switch) from it, so no
// production file has to change to be checked.
type sourceIndex struct {
	fset     *token.FileSet
	mod      string
	packages map[string]*sourcePackage // by full import path
	rels     map[string]string         // absolute file name -> path relative to the module root
}

type sourcePackage struct {
	Name   string
	Dir    string // relative to the module root
	Files  map[string]*sourceFile
	consts map[string]constDecl
}

type sourceFile struct {
	Rel     string // relative to the module root
	AST     *ast.File
	imports map[string]string // local name -> import path
}

type constDecl struct {
	value ast.Expr
	file  *sourceFile
	pkg   string
}

var (
	indexOnce sync.Once
	index     *sourceIndex
	indexErr  error
)

func loadSources(t *testing.T) *sourceIndex {
	t.Helper()
	root, mod := moduleRoot(t), modulePath(t)
	indexOnce.Do(func() { index, indexErr = parseModule(root, mod) })
	if indexErr != nil {
		t.Fatal(indexErr)
	}
	return index
}

func parseModule(root, mod string) (*sourceIndex, error) {
	idx := &sourceIndex{fset: token.NewFileSet(), mod: mod, packages: map[string]*sourcePackage{}, rels: map[string]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			if p != root {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir // another module
				}
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(idx.fset, p, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		idx.rels[p] = rel
		dir := path.Dir(rel)
		importPath := mod
		if dir != "." {
			importPath = mod + "/" + dir
		}
		pkg := idx.packages[importPath]
		if pkg == nil {
			pkg = &sourcePackage{Name: f.Name.Name, Dir: dir, Files: map[string]*sourceFile{}, consts: map[string]constDecl{}}
			idx.packages[importPath] = pkg
		}
		pkg.Files[rel] = &sourceFile{Rel: rel, AST: f}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for importPath, pkg := range idx.packages {
		for _, file := range pkg.Files {
			file.imports = map[string]string{}
			for _, spec := range file.AST.Imports {
				p, _ := strconv.Unquote(spec.Path.Value)
				local := path.Base(p)
				if target := idx.packages[p]; target != nil {
					local = target.Name
				}
				if spec.Name != nil {
					local = spec.Name.Name
				}
				file.imports[local] = p
			}
			for _, decl := range file.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					if len(vs.Values) != len(vs.Names) {
						continue
					}
					for i, n := range vs.Names {
						pkg.consts[n.Name] = constDecl{value: vs.Values[i], file: file, pkg: importPath}
					}
				}
			}
		}
	}
	return idx, nil
}

// stdlibStrings are the few standard-library constants route code uses.
var stdlibStrings = map[string]string{
	"net/http.MethodGet": "GET", "net/http.MethodPost": "POST", "net/http.MethodPut": "PUT",
	"net/http.MethodPatch": "PATCH", "net/http.MethodDelete": "DELETE", "net/http.MethodHead": "HEAD",
	"net/http.MethodOptions": "OPTIONS",
}

// eval evaluates a string expression made of literals, `+` and string
// constants of the module (or of the few in stdlibStrings). ok is false for
// anything else.
func (idx *sourceIndex) eval(e ast.Expr, pkgPath string, file *sourceFile) (string, bool) {
	return idx.evalDepth(e, pkgPath, file, 0)
}

func (idx *sourceIndex) evalDepth(e ast.Expr, pkgPath string, file *sourceFile, depth int) (string, bool) {
	if depth > 32 {
		return "", false
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return idx.evalDepth(e.X, pkgPath, file, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := idx.evalDepth(e.X, pkgPath, file, depth+1)
		if !ok {
			return "", false
		}
		y, ok := idx.evalDepth(e.Y, pkgPath, file, depth+1)
		return x + y, ok
	case *ast.Ident:
		return idx.evalConst(pkgPath, e.Name, depth)
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok || file == nil {
			return "", false
		}
		target, ok := file.imports[x.Name]
		if !ok {
			return "", false
		}
		if s, ok := stdlibStrings[target+"."+e.Sel.Name]; ok {
			return s, true
		}
		return idx.evalConst(target, e.Sel.Name, depth)
	}
	return "", false
}

// evalPartial is eval that writes "{?}" for every piece it cannot evaluate.
func (idx *sourceIndex) evalPartial(e ast.Expr, pkgPath string, file *sourceFile) string {
	if s, ok := idx.eval(e, pkgPath, file); ok {
		return s
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return idx.evalPartial(e.X, pkgPath, file)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return idx.evalPartial(e.X, pkgPath, file) + idx.evalPartial(e.Y, pkgPath, file)
		}
	}
	return "{?}"
}

func (idx *sourceIndex) evalConst(pkgPath, name string, depth int) (string, bool) {
	pkg := idx.packages[pkgPath]
	if pkg == nil {
		return "", false
	}
	c, ok := pkg.consts[name]
	if !ok {
		return "", false
	}
	return idx.evalDepth(c.value, c.pkg, c.file, depth+1)
}

// pos is a short file:line for messages, relative to the module root.
func (idx *sourceIndex) pos(n ast.Node) string {
	p := idx.fset.Position(n.Pos())
	return fmt.Sprintf("%s:%d", idx.rels[p.Filename], p.Line)
}
