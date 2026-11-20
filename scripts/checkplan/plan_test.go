package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const mod = "example.com/m"

// testGraph is a small module:
//
//	core            imported by store and agent
//	store           imported by server
//	server          imported by cmd/amux
//	agent           test-imported by integrations (only in its tests)
//	internal/archguard, internal/postgressetup, platform/feishu, web: leaves
func testGraph() *graph {
	ip := func(dir string) string { return mod + "/" + dir }
	return &graph{Module: mod, Pkgs: []goPackage{
		{Dir: "agent", ImportPath: ip("agent"), Imports: []string{ip("core"), "fmt"}},
		{Dir: "cmd/amux", ImportPath: ip("cmd/amux"), Imports: []string{ip("server")}},
		{Dir: "core", ImportPath: ip("core"), Imports: []string{"net/http"}, TestImports: []string{"testing"}},
		{Dir: "integrations", ImportPath: ip("integrations"), TestImports: []string{ip("agent")}},
		{Dir: "internal/archguard", ImportPath: ip("internal/archguard")},
		{Dir: "internal/postgressetup", ImportPath: ip("internal/postgressetup")},
		{Dir: "platform/feishu", ImportPath: ip("platform/feishu")},
		{Dir: "server", ImportPath: ip("server"), Imports: []string{ip("store"), ip("gone")}},
		{Dir: "store", ImportPath: ip("store"), Imports: []string{ip("core")}},
		{Dir: "web", ImportPath: ip("web")},
	}}
}

// stepsOf renders steps as "target" or "target: pkg pkg".
func stepsOf(p plan) []string {
	var out []string
	for _, s := range p.Steps {
		if s.Pkgs == nil {
			out = append(out, s.Target)
			continue
		}
		out = append(out, s.Target+": "+strings.Join(s.Pkgs, " "))
	}
	return out
}

func TestMakePlan(t *testing.T) {
	every := []string{"test", "vet", "staticcheck", "tidy-check", "web-test", "web"}
	for _, tc := range []struct {
		name  string
		files []string
		want  []string
	}{
		{
			name:  "docs only",
			files: []string{"docs/architecture/guards.md", "README.md", "AGENTS.md"},
			want:  nil,
		},
		{
			name:  "web only",
			files: []string{"web/src/App.tsx", "web/src/i18n/zh.ts", "web/package.json"},
			want:  []string{"web-test", "web"},
		},
		{
			name:  "web Go file",
			files: []string{"web/embed.go"},
			want: []string{
				"test: internal/archguard web",
				"vet: web",
				"staticcheck: web",
				"web-test",
				"web",
			},
		},
		{
			name:  "core change pulls in its importers and their test importers",
			files: []string{"core/agent.go"},
			want: []string{
				"test: agent cmd/amux core integrations internal/archguard server store",
				"vet: agent cmd/amux core integrations server store",
				"staticcheck: agent cmd/amux core integrations server store",
			},
		},
		{
			name:  "leaf change stays in its package",
			files: []string{"cmd/amux/usage.go"},
			want: []string{
				"test: cmd/amux internal/archguard",
				"vet: cmd/amux",
				"staticcheck: cmd/amux",
			},
		},
		{
			name:  "go.mod",
			files: []string{"go.mod"},
			want:  []string{"test", "vet", "staticcheck", "tidy-check"},
		},
		{
			name:  "go.sum with a Go change",
			files: []string{"go.sum", "store/db.go"},
			want:  []string{"test", "vet", "staticcheck", "tidy-check"},
		},
		{
			name:  "Makefile runs everything",
			files: []string{"Makefile"},
			want:  every,
		},
		{
			name:  "the plan itself runs everything",
			files: []string{"scripts/checkplan/plan.go"},
			want:  every,
		},
		{
			name:  "CI workflow runs everything",
			files: []string{".github/workflows/ci.yml"},
			want:  every,
		},
		{
			name:  "staticcheck config",
			files: []string{"staticcheck.conf"},
			want:  []string{"staticcheck"},
		},
		{
			name:  "testdata selects the package that owns it",
			files: []string{"store/testdata/fixture.json"},
			want: []string{
				"test: cmd/amux server store",
				"vet: cmd/amux server store",
				"staticcheck: cmd/amux server store",
			},
		},
		{
			name:  "Go file under testdata is data",
			files: []string{"core/testdata/sample/main.go"},
			want: []string{
				"test: agent cmd/amux core integrations server store",
				"vet: agent cmd/amux core integrations server store",
				"staticcheck: agent cmd/amux core integrations server store",
			},
		},
		{
			name:  "root file a test reads",
			files: []string{"install.sh"},
			want: []string{
				"test: internal/postgressetup",
				"vet: internal/postgressetup",
				"staticcheck: internal/postgressetup",
			},
		},
		{
			name:  "deleted package still selects its former importers",
			files: []string{"gone/gone.go"},
			want: []string{
				"test: cmd/amux internal/archguard server",
				"vet: cmd/amux server",
				"staticcheck: cmd/amux server",
			},
		},
		{
			name:  "desktop shell is not part of make check",
			files: []string{"desktop/app.go", "desktop/frontend/sync-web-dist.mjs"},
			want:  []string{"test: internal/archguard"},
		},
		{
			name:  "apps and SDKs outside make check",
			files: []string{"android/app/build.gradle", "ios/AgentMux/App.swift", "macos-menubar/main.swift", "sdk/typescript/src/index.ts"},
			want:  nil,
		},
		{
			name:  "nothing changed",
			files: nil,
			want:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stepsOf(makePlan(tc.files, testGraph()))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("plan for %v\n got %q\nwant %q", tc.files, got, tc.want)
			}
		})
	}
}

func TestMakePlanNotes(t *testing.T) {
	p := makePlan([]string{"docs/a.md", "README.md", "go.mod", "core/x.go"}, testGraph())
	want := []fileNote{
		{"docs/a.md", "no check: docs (no link checker in this repository)"},
		{"README.md", "no check: not part of make check"},
		{"go.mod", "tidy-check, test, vet, staticcheck on every Go package"},
		{"core/x.go", "Go package ./core"},
	}
	if !reflect.DeepEqual(p.Files, want) {
		t.Errorf("notes\n got %q\nwant %q", p.Files, want)
	}
}

// Without a package graph, anything that touches Go checks every package
// rather than fewer.
func TestMakePlanWithoutGraph(t *testing.T) {
	got := stepsOf(makePlan([]string{"core/agent.go", "web/src/App.tsx"}, nil))
	want := []string{"test", "vet", "staticcheck", "web-test", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := stepsOf(makePlan([]string{"docs/a.md"}, nil)); got != nil {
		t.Errorf("docs without a graph: got %q, want nothing", got)
	}
}

// The real module: a core change selects packages beyond core, among them
// its importers server and store.
func TestRealGraphCoreImporters(t *testing.T) {
	root := repoRoot(t)
	g, err := loadGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	p := makePlan([]string{"core/types.go"}, g)
	if len(p.Steps) != 3 || p.Steps[0].Target != checkTest {
		t.Fatalf("steps = %q, want test, vet, staticcheck", stepsOf(p))
	}
	for _, want := range []string{"core", "server", "store", archguardDir} {
		if !slices.Contains(p.Steps[0].Pkgs, want) {
			t.Errorf("test packages %q lack %s", p.Steps[0].Pkgs, want)
		}
	}
	if len(p.Steps[0].Pkgs) >= len(g.Pkgs) && len(g.Pkgs) > 0 {
		t.Logf("a core change selects all %d packages", len(g.Pkgs))
	}
}

// Every path the table names exists, so a rename cannot leave a rule behind.
func TestRulePathsExist(t *testing.T) {
	root := repoRoot(t)
	for _, r := range rules {
		paths := append([]string{r.path}, r.pkgs...)
		for _, p := range paths {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
				t.Errorf("rule %q: %v", r.path, err)
			}
		}
	}
}

// allChecks is what the Makefile's full `check` rule runs.
func TestChecksMatchMakefile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "check: "); ok {
			found = strings.Fields(rest)
		}
	}
	if !reflect.DeepEqual(found, allChecks) {
		t.Errorf("Makefile check: %q, allChecks %q", found, allChecks)
	}
}

// changedFiles sees committed, staged, unstaged and untracked changes, and
// both sides of a rename, but not ignored files.
func TestChangedFiles(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// No global config or hooks: the user's commit hooks are slow.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write(".gitignore", "ignored.txt\n")
	write("same.txt", "same\n")
	write("old/name.txt", "renamed\n")
	write("edited.txt", "v1\n")
	write("staged.txt", "v1\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")

	git("checkout", "-q", "-b", "topic")
	write("committed.go", "package x\n")
	if err := os.Mkdir(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "old/name.txt", "new/name.txt")
	git("add", "-A")
	git("commit", "-q", "-m", "topic")
	write("staged.txt", "v2\n")
	git("add", "staged.txt")
	write("edited.txt", "v2\n")
	write("untracked/file.md", "new\n")
	write("ignored.txt", "x\n")

	got, err := changedFiles(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"committed.go", "edited.txt", "new/name.txt", "old/name.txt", "staged.txt", "untracked/file.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changedFiles\n got %q\nwant %q", got, want)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}
