package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// MarketplaceSkill is a public skill catalog entry.
type MarketplaceSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
	Source      string `json:"source"`
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	URL         string `json:"url,omitempty"`
	Trusted     bool   `json:"trusted"`
	Installed   bool   `json:"installed"`
}

// InstallRequest describes a marketplace installation.
type InstallRequest struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var marketplace = []MarketplaceSkill{
	{
		Name: "skill-creator", Category: "meta", Source: "openai", Repo: "openai/skills",
		Path:        "skills/.system/skill-creator",
		Description: "Create reusable Codex skills from a workflow or written instructions.",
		URL:         "https://github.com/openai/skills/tree/main/skills/.system/skill-creator",
		Trusted:     true,
	},
	{
		Name: "pdf", Category: "documents", Source: "anthropic", Repo: "anthropics/skills",
		Path:        "skills/pdf",
		Description: "Read, extract, merge, annotate, and analyze PDF files.",
		URL:         "https://github.com/anthropics/skills/tree/main/skills/pdf",
		Trusted:     true,
	},
	{
		Name: "docx", Category: "documents", Source: "anthropic", Repo: "anthropics/skills",
		Path:        "skills/docx",
		Description: "Create, edit, analyze, and comment on Word documents.",
		URL:         "https://github.com/anthropics/skills/tree/main/skills/docx",
		Trusted:     true,
	},
	{
		Name: "xlsx", Category: "data", Source: "anthropic", Repo: "anthropics/skills",
		Path:        "skills/xlsx",
		Description: "Manipulate spreadsheets, formulas, charts, and tabular data.",
		URL:         "https://github.com/anthropics/skills/tree/main/skills/xlsx",
		Trusted:     true,
	},
	{
		Name: "pptx", Category: "presentations", Source: "anthropic", Repo: "anthropics/skills",
		Path:        "skills/pptx",
		Description: "Read, generate, and adjust presentation decks.",
		URL:         "https://github.com/anthropics/skills/tree/main/skills/pptx",
		Trusted:     true,
	},
	{
		Name: "web-artifacts-builder", Category: "development", Source: "anthropic", Repo: "anthropics/skills",
		Path:        "skills/web-artifacts-builder",
		Description: "Build rich HTML artifacts with modern frontend tooling.",
		URL:         "https://github.com/anthropics/skills/tree/main/skills/web-artifacts-builder",
		Trusted:     true,
	},
	{
		Name: "brooks-lint", Category: "development", Source: "awesome-codex-skills", Repo: "hyhmrright/brooks-lint",
		Path:        "skills/brooks-lint",
		Description: "Run engineering-quality reviews grounded in classic software engineering books.",
		URL:         "https://github.com/hyhmrright/brooks-lint/tree/main/skills/brooks-lint",
		Trusted:     false,
	},
	{
		Name: "bringyour-migration-auditor", Category: "development", Source: "awesome-codex-skills", Repo: "unitedideas/bringyour-mcp",
		Path:        "skills/bringyour-migration-auditor",
		Description: "Audit Claude Code to Codex migration details for prompts, hooks, MCP, and skills.",
		URL:         "https://github.com/unitedideas/bringyour-mcp/tree/main/skills/bringyour-migration-auditor",
		Trusted:     false,
	},
	{
		Name: "test-driven-development", Category: "development", Source: "awesome-claude-skills", Repo: "obra/superpowers",
		Path:        "skills/test-driven-development",
		Description: "Guide implementation through a test-first development loop.",
		URL:         "https://github.com/obra/superpowers/tree/main/skills/test-driven-development",
		Trusted:     false,
	},
}

// Marketplace returns catalog entries filtered by query/source/category.
func (m *FSManager) Marketplace(ctx context.Context, query, source, category string) ([]MarketplaceSkill, error) {
	installed, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	installedNames := map[string]bool{}
	for _, skill := range installed {
		installedNames[skill.Name] = true
	}
	query = strings.ToLower(strings.TrimSpace(query))
	source = strings.ToLower(strings.TrimSpace(source))
	category = strings.ToLower(strings.TrimSpace(category))
	out := make([]MarketplaceSkill, 0, len(marketplace))
	for _, item := range marketplace {
		if source != "" && strings.ToLower(item.Source) != source {
			continue
		}
		if category != "" && strings.ToLower(item.Category) != category {
			continue
		}
		haystack := strings.ToLower(item.Name + " " + item.Description + " " + item.Category + " " + item.Source)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		item.Installed = installedNames[item.Name]
		out = append(out, item)
	}
	return out, nil
}

// InstallMarketplace installs one GitHub repo/path skill into the global root.
func (m *FSManager) InstallMarketplace(ctx context.Context, req InstallRequest) (*core.Skill, error) {
	repo, subdir, err := validateGitHubRequest(req)
	if err != nil {
		return nil, err
	}
	targetRoot := ""
	if len(m.roots) > 0 {
		targetRoot = m.roots[0]
	}
	if targetRoot == "" {
		targetRoot = DefaultRoots()[0]
	}
	tmp, err := os.MkdirTemp("", "agentmux-skill-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	cloneDir := filepath.Join(tmp, "repo")
	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "git", "clone", "--depth=1", "https://github.com/"+repo+".git", cloneDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	src := filepath.Join(cloneDir, filepath.FromSlash(subdir))
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("skills: no SKILL.md at %s/%s: %w", repo, subdir, err)
	}
	s := ParseSkillFile(filepath.Join(src, "SKILL.md"))
	if req.Name != "" {
		s.Name = strings.TrimSpace(req.Name)
	}
	if s.Name == "" {
		s.Name = filepath.Base(src)
	}
	dst := filepath.Join(targetRoot, s.Name)
	if err := copyDir(src, dst); err != nil {
		return nil, err
	}
	s = ParseSkillFile(filepath.Join(dst, "SKILL.md"))
	if s.Name == "" {
		s.Name = filepath.Base(dst)
	}
	s.Source = "github:" + repo
	s.Enabled = true
	return &s, nil
}

func validateGitHubRequest(req InstallRequest) (string, string, error) {
	repo := strings.TrimSpace(req.Repo)
	if !githubRepoPattern.MatchString(repo) {
		return "", "", fmt.Errorf("repo must be owner/name")
	}
	subdir := strings.Trim(strings.TrimSpace(req.Path), "/")
	if subdir == "" || strings.HasPrefix(subdir, ".git") {
		return "", "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(subdir))
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", "", fmt.Errorf("path must stay inside the repository")
	}
	return repo, filepath.ToSlash(clean), nil
}
