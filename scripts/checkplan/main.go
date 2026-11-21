// Command checkplan runs the checks of `make check` that the changes since the
// merge base with a ref need: `make check BASE=<ref>`.
//
// It collects the committed, uncommitted and untracked files changed since
// that merge base, maps them to checks through the table in plan.go, prints
// the plan, and with -run runs the selected checks in parallel. Go checks run
// on the changed packages, their importers and the packages whose tests
// import those.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

func main() {
	base := flag.String("base", "", "compare with the merge base of HEAD and this ref")
	run := flag.Bool("run", false, "run the planned checks")
	makeCmd := flag.String("make", "make", "the make command that runs a check")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "usage: checkplan -base <ref> [-run] [-make make]")
		os.Exit(2)
	}
	os.Exit(mainErr(*base, *run, *makeCmd))
}

func mainErr(base string, run bool, makeCmd string) int {
	root, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkplan:", err)
		return 2
	}
	root = strings.TrimSpace(root)
	mergeBase, err := gitOutput(root, "merge-base", base+"^{commit}", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkplan: no merge base of HEAD and %q: %v\n", base, err)
		return 2
	}
	mergeBase = strings.TrimSpace(mergeBase)
	files, err := changedFiles(root, mergeBase)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkplan:", err)
		return 2
	}
	g, err := loadGraph(root)
	if err != nil {
		// Never select less because the packages could not be listed.
		fmt.Fprintf(os.Stderr, "checkplan: listing Go packages failed, so Go checks run on every package: %v\n", err)
		g = nil
	}
	p := makePlan(files, g)
	printPlan(os.Stdout, base, mergeBase, p)
	if !run || len(p.Steps) == 0 {
		return 0
	}
	if !runSteps(os.Stdout, root, makeCmd, p.Steps) {
		return 1
	}
	return 0
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// changedFiles lists, relative to root, the files that differ between the
// merge base and the working tree (committed, staged or not) and the
// untracked files git does not ignore. Both sides of a rename are listed.
func changedFiles(root, mergeBase string) ([]string, error) {
	diff, err := gitOutput(root, "diff", "--name-only", "--no-renames", "-z", mergeBase, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := gitOutput(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(diff+untracked, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// loadGraph lists the module's packages with `go list -e`, so that a package
// that no longer builds still reports what it imports.
func loadGraph(root string) (*graph, error) {
	mod := exec.Command("go", "list", "-m")
	mod.Dir = root
	modOut, err := mod.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m: %w", err)
	}
	cmd := exec.Command("go", "list", "-e", "-json=ImportPath,Dir,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	g := &graph{Module: strings.TrimSpace(string(modOut))}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p struct {
			ImportPath, Dir                    string
			Imports, TestImports, XTestImports []string
		}
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list ./...: %w", err)
		}
		rel, err := filepath.Rel(root, p.Dir)
		if err != nil {
			return nil, err
		}
		g.Pkgs = append(g.Pkgs, goPackage{
			Dir:         filepath.ToSlash(rel),
			ImportPath:  p.ImportPath,
			Imports:     p.Imports,
			TestImports: append(p.TestImports, p.XTestImports...),
		})
	}
	return g, nil
}

func printPlan(w io.Writer, base, mergeBase string, p plan) {
	fmt.Fprintf(w, "Check plan: %d changed %s since %.10s (merge base with %s)\n", len(p.Files), plural(len(p.Files), "file", "files"), mergeBase, base)
	width := 0
	for _, f := range p.Files {
		width = max(width, len(f.File))
	}
	width = min(width, 56)
	const maxFiles = 40
	for i, f := range p.Files {
		if i == maxFiles {
			fmt.Fprintf(w, "  … and %d more\n", len(p.Files)-maxFiles)
			break
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, f.File, f.Note)
	}
	if len(p.Steps) == 0 {
		fmt.Fprintln(w, "Nothing to check.")
		return
	}
	fmt.Fprintf(w, "Runs %d of %d checks in parallel:\n", len(p.Steps), len(allChecks))
	for _, s := range p.Steps {
		if d := describeStep(s, p.Changed); d != "" {
			fmt.Fprintf(w, "  %-12s %s\n", s.Target, d)
		} else {
			fmt.Fprintf(w, "  %s\n", s.Target)
		}
	}
	var skipped []string
	for _, c := range allChecks {
		if !slices.ContainsFunc(p.Steps, func(s step) bool { return s.Target == c }) {
			skipped = append(skipped, c)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "Skips: %s\n", strings.Join(skipped, ", "))
	}
}

func describeStep(s step, changed []string) string {
	if !slices.Contains(goChecks, s.Target) {
		return ""
	}
	if s.Pkgs == nil {
		return "every package (./...)"
	}
	direct := 0
	for _, d := range s.Pkgs {
		if slices.Contains(changed, d) {
			direct++
		}
	}
	return fmt.Sprintf("%d %s (%d changed, %d affected): %s", len(s.Pkgs), plural(len(s.Pkgs), "package", "packages"),
		direct, len(s.Pkgs)-direct, strings.Join(s.Pkgs, " "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// goPkgsArg is the GO_PKGS value for a step.
func goPkgsArg(s step) string {
	args := make([]string, len(s.Pkgs))
	for i, d := range s.Pkgs {
		args[i] = pkgArg(d)
	}
	return strings.Join(args, " ")
}

type result struct {
	step     step
	err      error
	output   []byte
	duration time.Duration
}

// runSteps runs the steps in parallel through make, printing each failing
// step's output once it ends, then a summary. It reports whether all passed.
func runSteps(w io.Writer, root, makeCmd string, steps []step) bool {
	start := time.Now()
	makeArgs := func(target string, extra ...string) *exec.Cmd {
		cmd := exec.Command(makeCmd, append([]string{"--no-print-directory", target}, extra...)...)
		cmd.Dir = root
		return cmd
	}
	// Both Console checks need the npm install; do it once, before them.
	if slices.ContainsFunc(steps, func(s step) bool { return s.Target == checkWebTest || s.Target == checkWeb }) {
		fmt.Fprintln(w, "==> web-deps")
		cmd := makeArgs("web-deps")
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(w, "FAIL web-deps: %v\n", err)
			return false
		}
	}
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Target
	}
	fmt.Fprintf(w, "==> running %s\n", strings.Join(names, ", "))

	results := make([]result, len(steps))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, s := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var extra []string
			if s.Pkgs != nil {
				extra = append(extra, "GO_PKGS="+goPkgsArg(s))
			}
			cmd := makeArgs(s.Target, extra...)
			t0 := time.Now()
			out, err := cmd.CombinedOutput()
			r := result{step: s, err: err, output: out, duration: time.Since(t0)}
			results[i] = r
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Fprintf(w, "--- FAIL %s (%s)\n", s.Target, round(r.duration))
				w.Write(out)
				if len(out) > 0 && out[len(out)-1] != '\n' {
					fmt.Fprintln(w)
				}
			} else {
				fmt.Fprintf(w, "--- pass %s (%s)\n", s.Target, round(r.duration))
			}
		}()
	}
	wg.Wait()

	failed := 0
	fmt.Fprintln(w, "Summary:")
	for _, r := range results {
		status := "PASS"
		if r.err != nil {
			status = "FAIL"
			failed++
		}
		fmt.Fprintf(w, "  %s  %-12s %s\n", status, r.step.Target, round(r.duration))
	}
	if failed > 0 {
		fmt.Fprintf(w, "%d of %d checks failed in %s\n", failed, len(results), round(time.Since(start)))
		return false
	}
	fmt.Fprintf(w, "All %d checks passed in %s\n", len(results), round(time.Since(start)))
	return true
}

func round(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}
