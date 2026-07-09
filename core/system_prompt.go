package core

import "strings"

// CLINote describes an enabled CLI tool for prompt injection.
type CLINote struct {
	Name string
	Note string
}

// ComposeSystemPrompt builds the final system prompt injected into an agent
// runtime. It starts from the user-configured base prompt and appends a section
// describing the channel event-callback log paths and any enabled CLI tools.
// Empty inputs are skipped so the base prompt is returned unchanged when there
// is nothing to inject.
func ComposeSystemPrompt(base string, logPaths []string, clis []CLINote) string {
	var b strings.Builder
	base = strings.TrimRight(base, "\n")
	if base != "" {
		b.WriteString(base)
	}

	var sections []string

	if len(logPaths) > 0 {
		var s strings.Builder
		s.WriteString("绑定的事件回调日志路径为：")
		for _, p := range logPaths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			s.WriteString("\n- ")
			s.WriteString(p)
		}
		sections = append(sections, s.String())
	}

	if len(clis) > 0 {
		var s strings.Builder
		s.WriteString("已启用以下 CLI 工具：")
		for _, c := range clis {
			name := strings.TrimSpace(c.Name)
			if name == "" {
				continue
			}
			s.WriteString("\n- ")
			s.WriteString(name)
			if note := strings.TrimSpace(c.Note); note != "" {
				s.WriteString("：")
				s.WriteString(note)
			}
		}
		sections = append(sections, s.String())
	}

	for _, section := range sections {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
	}

	return b.String()
}
