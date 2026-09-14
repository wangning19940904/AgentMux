package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditSkillDescriptionPreservesInstructionsAndMetadata(t *testing.T) {
	for _, header := range []string{
		"---\nname: demo\ndescription: >-\n  Old description\n  on two lines.\nlicense: MIT\n---\n",
		"---\nname: demo\ndescription: \"old: description\"\nlicense: MIT\n---\n",
		"",
	} {
		t.Run(header, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "demo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "SKILL.md")
			body := "# Original instructions\r\n\r\n```yaml\r\nname: example\r\ndescription: example only\r\n```\r\n"
			if err := os.WriteFile(path, []byte(header+body), 0o640); err != nil {
				t.Fatal(err)
			}
			m := New(root)
			// Files without frontmatter retain the legacy discovery name.
			name := ParseSkillFile(path).Name
			for _, description := range []string{"自定义: 描述\n第二行 \"引号\" # text", ""} {
				if err := m.SetDescription(context.Background(), name, description); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(string(data), body) || (header != "" && !strings.Contains(string(data), "license: MIT")) {
					t.Fatalf("unrelated skill content changed: %s", data)
				}
				parsed := ParseSkillFile(path)
				if parsed.Name != name || parsed.Description != description {
					t.Fatalf("metadata = %+v", parsed)
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0o640 {
					t.Fatalf("mode = %o", info.Mode().Perm())
				}
			}
		})
	}
}

func TestEditSkillDescriptionRefusesExternalSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "SKILL.md")
	original := "---\nname: demo\ndescription: original\n---\n"
	if err := os.WriteFile(external, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := New(root).SetDescription(context.Background(), "demo", "replacement"); err == nil {
		t.Fatal("expected external path rejection")
	}
	data, _ := os.ReadFile(external)
	if string(data) != original {
		t.Fatal("external file was modified")
	}
}
