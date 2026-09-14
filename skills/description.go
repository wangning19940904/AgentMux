package skills

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/store"
	"gopkg.in/yaml.v3"
)

// SetDescription updates the skill metadata consumed by the runtime through
// its managed workspace link. The instruction body is preserved verbatim.
func (m *FSManager) SetDescription(ctx context.Context, name, description string) error {
	items, err := m.List(ctx)
	if err != nil {
		return err
	}
	for _, skill := range items {
		if skill.Name != name {
			continue
		}
		path, err := filepath.EvalSymlinks(skill.Path)
		if err != nil {
			return err
		}
		managed := false
		for _, root := range m.roots {
			resolved, err := filepath.EvalSymlinks(root)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(resolved, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				managed = true
				break
			}
		}
		if !managed {
			return fmt.Errorf("skills: refusing to edit %q outside managed roots", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		header, body, hasHeader, err := splitSkillFrontmatter(data)
		if err != nil {
			return err
		}
		mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if hasHeader {
			var document yaml.Node
			if err := yaml.Unmarshal(header, &document); err != nil {
				return fmt.Errorf("skills: invalid frontmatter: %w", err)
			}
			if len(document.Content) > 0 {
				mapping = document.Content[0]
				if mapping.Kind != yaml.MappingNode {
					return fmt.Errorf("skills: frontmatter must be a mapping")
				}
			}
		} else {
			mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "name"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
		}
		value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(description)}
		found := false
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == "description" {
				mapping.Content[i+1] = value
				found = true
			}
		}
		if !found {
			mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "description"}, value)
		}
		var output bytes.Buffer
		output.WriteString("---\n")
		encoder := yaml.NewEncoder(&output)
		encoder.SetIndent(2)
		if err := encoder.Encode(mapping); err != nil {
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		output.WriteString("---\n")
		output.Write(body)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		return store.AtomicWrite(path, output.Bytes(), info.Mode().Perm())
	}
	return fmt.Errorf("skills: %q: %w", name, os.ErrNotExist)
}

func splitSkillFrontmatter(data []byte) (header, body []byte, present bool, err error) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) == 0 || strings.TrimSpace(string(lines[0])) != "---" {
		return nil, data, false, nil
	}
	offset := len(lines[0])
	for _, line := range lines[1:] {
		if strings.TrimSpace(string(line)) == "---" {
			return data[len(lines[0]):offset], data[offset+len(line):], true, nil
		}
		offset += len(line)
	}
	return nil, nil, true, fmt.Errorf("skills: unclosed frontmatter")
}
