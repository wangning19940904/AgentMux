package archguard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// server/ should be HTTP transport only: handlers decode, authorize and encode
// (docs/architecture/hub-and-super-agents.md §1 and §8). Some of its files
// still hold business logic the doc wants moved into services. This ratchet
// records their non-test line counts in testdata/server_size.txt, so they can
// shrink but not grow.
//
// Each line is "<glob> <lines>": a file name or glob inside server/ (non-test
// files only) and the number of lines its files hold together. The last line,
// "total <lines>", is the sum over every file the globs match, each counted
// once. A count must equal the code: above it the business logic grew, below
// it the file shrank and the ceiling must come down with it, so the gain
// cannot be spent again later.
func TestServerSizeRatchet(t *testing.T) {
	serverDir := filepath.Join(moduleRoot(t), "server")
	all, err := filepath.Glob(filepath.Join(serverDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, filepath.Base(f))
		}
	}
	lines := map[string]int{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(serverDir, f))
		if err != nil {
			t.Fatal(err)
		}
		n := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
		lines[f] = n
	}

	counted := map[string]string{} // file -> the glob that counts it
	var total *listLine
	totalCeiling := 0
	for _, l := range readList(t, "server_size.txt") {
		glob, num, ok := strings.Cut(l.Text, " ")
		ceiling, err := strconv.Atoi(num)
		if !ok || err != nil || strings.Contains(num, " ") {
			t.Errorf("testdata/server_size.txt:%d: want \"<glob> <lines>\", got %q", l.Line, l.Text)
			continue
		}
		if glob == "total" {
			if total != nil {
				t.Errorf("testdata/server_size.txt:%d: a second total line", l.Line)
			}
			total, totalCeiling = &l, ceiling
			continue
		}
		if _, err := filepath.Match(glob, ""); err != nil || strings.Contains(glob, "/") {
			t.Errorf("testdata/server_size.txt:%d: %q is not a file glob inside server/", l.Line, glob)
			continue
		}
		var matched []string
		sum := 0
		for _, f := range files {
			if ok, _ := filepath.Match(glob, f); ok {
				// A file counted by two lines would let one ceiling hide
				// another's growth.
				if other, dup := counted[f]; dup {
					t.Errorf("testdata/server_size.txt:%d: %s is already counted by %q: make the globs disjoint", l.Line, f, other)
				}
				matched = append(matched, f)
				sum += lines[f]
				counted[f] = glob
			}
		}
		if len(matched) == 0 {
			t.Errorf("testdata/server_size.txt:%d: %q matches no non-test file in server/ any more: delete the line and lower the total; %s",
				l.Line, glob, guardDoc)
			continue
		}
		checkCeiling(t, l, glob, strings.Join(matched, ", "), sum, ceiling)
	}
	if total == nil {
		t.Fatal(`testdata/server_size.txt has no "total <lines>" line`)
	}
	sum := 0
	for f := range counted {
		sum += lines[f]
	}
	checkCeiling(t, *total, "total", fmt.Sprintf("%d files", len(counted)), sum, totalCeiling)

}

func checkCeiling(t *testing.T, l listLine, name, files string, actual, ceiling int) {
	t.Helper()
	switch {
	case actual > ceiling:
		t.Errorf("server/ %s (%s) has %d lines, over its ceiling of %d (testdata/server_size.txt:%d): this is business logic "+
			"server/ should shed, so put the new code in a service package and call it from the handler; %s",
			name, files, actual, ceiling, l.Line, guardDoc)
	case actual < ceiling:
		t.Errorf("server/ %s (%s) shrank to %d lines: lower its ceiling in testdata/server_size.txt:%d from %d to %d "+
			"(and the total by the same amount) so it stays down; %s",
			name, files, actual, l.Line, ceiling, actual, guardDoc)
	}
}
