package remote

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DiscoveredHost is a concrete host alias found in the current user's
// OpenSSH configuration. It intentionally contains configuration metadata
// only; AgentMux never reads or returns private-key contents.
type DiscoveredHost struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	KeyPath      string `json:"key_path,omitempty"`
	SSHAlias     string `json:"ssh_alias"`
	Source       string `json:"source"`
	ProxyJump    string `json:"proxy_jump,omitempty"`
	ProxyCommand bool   `json:"proxy_command,omitempty"`
}

type sshConfigDirective struct {
	key    string
	values []string
	source string
}

// DiscoverSSHHosts reads ~/.ssh/config (including Include files) and returns
// concrete Host aliases. Wildcard-only entries are defaults, not connection
// targets, so they are applied to aliases but are not returned themselves.
func DiscoverSSHHosts(configPath string) ([]DiscoveredHost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(home, ".ssh", "config")
	} else {
		configPath, err = expandSSHPath(configPath, home)
		if err != nil {
			return nil, err
		}
	}

	directives, err := readSSHConfig(configPath, filepath.Join(home, ".ssh"), nil, 0)
	if errors.Is(err, os.ErrNotExist) {
		return []DiscoveredHost{}, nil
	}
	if err != nil {
		return nil, err
	}

	type aliasInfo struct {
		name   string
		source string
	}
	aliases := make([]aliasInfo, 0)
	seen := make(map[string]bool)
	for _, directive := range directives {
		if directive.key != "host" {
			continue
		}
		for _, candidate := range directive.values {
			if !isConcreteSSHAlias(candidate) {
				continue
			}
			key := strings.ToLower(candidate)
			if seen[key] {
				continue
			}
			seen[key] = true
			aliases = append(aliases, aliasInfo{name: candidate, source: directive.source})
		}
	}

	currentUser := ""
	if account, lookupErr := user.Current(); lookupErr == nil {
		currentUser = account.Username
	}
	if currentUser == "" {
		currentUser = strings.TrimSpace(os.Getenv("USER"))
	}

	hosts := make([]DiscoveredHost, 0, len(aliases))
	for _, alias := range aliases {
		host := resolveSSHHost(alias.name, alias.source, directives, home, currentUser)
		if host.User == "" {
			continue
		}
		if _, err := normalizeHost(Host{
			ID: "discovered", Name: host.Name, Host: host.Host, Port: host.Port,
			User: host.User, KeyPath: host.KeyPath, RemoteAddr: defaultRemoteAddr,
		}); err != nil {
			// Keep malformed or token-heavy aliases out of the import UI. The
			// source config remains untouched and is still available to ssh(1).
			continue
		}
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return strings.ToLower(hosts[i].Name) < strings.ToLower(hosts[j].Name)
	})
	return hosts, nil
}

func readSSHConfig(path, includeBase string, stack map[string]bool, depth int) ([]sshConfigDirective, error) {
	if depth > 32 {
		return nil, fmt.Errorf("SSH config include depth exceeds 32")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SSH config %s: %w", path, err)
	}
	if stack == nil {
		stack = make(map[string]bool)
	}
	if stack[absolute] {
		return nil, fmt.Errorf("SSH config include cycle at %s", displaySSHPath(absolute))
	}

	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stack[absolute] = true
	defer delete(stack, absolute)

	var directives []sshConfigDirective
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := splitSSHConfigFields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		key, values := sshDirectiveParts(fields)
		if key == "" || len(values) == 0 {
			continue
		}
		if key != "include" {
			directives = append(directives, sshConfigDirective{
				key: key, values: values, source: displaySSHPath(absolute),
			})
			continue
		}
		for _, pattern := range values {
			expanded, expandErr := expandSSHInclude(pattern, includeBase)
			if expandErr != nil {
				return nil, fmt.Errorf("expand SSH Include %q: %w", pattern, expandErr)
			}
			matches, globErr := filepath.Glob(expanded)
			if globErr != nil {
				return nil, fmt.Errorf("expand SSH Include %q: %w", pattern, globErr)
			}
			sort.Strings(matches)
			for _, match := range matches {
				info, statErr := os.Stat(match)
				if statErr != nil || info.IsDir() {
					continue
				}
				included, includeErr := readSSHConfig(match, includeBase, stack, depth+1)
				if includeErr != nil {
					return nil, fmt.Errorf("read included SSH config %s: %w", displaySSHPath(match), includeErr)
				}
				directives = append(directives, included...)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSH config %s: %w", displaySSHPath(absolute), err)
	}
	return directives, nil
}

func splitSSHConfigFields(line string) []string {
	var fields []string
	var field strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if field.Len() > 0 {
			fields = append(fields, field.String())
			field.Reset()
		}
	}
	for _, char := range line {
		if escaped {
			field.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				field.WriteRune(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case '#':
			flush()
			return fields
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			field.WriteRune(char)
		}
	}
	if escaped {
		field.WriteRune('\\')
	}
	flush()
	return fields
}

func sshDirectiveParts(fields []string) (string, []string) {
	if len(fields) == 0 {
		return "", nil
	}
	key := fields[0]
	values := fields[1:]
	if before, after, found := strings.Cut(key, "="); found {
		key = before
		if after != "" {
			values = append([]string{after}, values...)
		}
	} else if len(values) > 0 && values[0] == "=" {
		values = values[1:]
	}
	return strings.ToLower(strings.TrimSpace(key)), values
}

func isConcreteSSHAlias(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	return candidate != "" &&
		!strings.HasPrefix(candidate, "!") &&
		!strings.ContainsAny(candidate, "*?!") &&
		!strings.ContainsAny(candidate, "/\\ \t\r\n")
}

func resolveSSHHost(alias, source string, directives []sshConfigDirective, home, currentUser string) DiscoveredHost {
	host := DiscoveredHost{
		Name: alias, Host: alias, Port: 22, User: currentUser, SSHAlias: alias, Source: source,
	}
	active := true
	hostSet, portSet, userSet, keySet, proxySet, proxyCommandSet := false, false, false, false, false, false
	for _, directive := range directives {
		switch directive.key {
		case "host":
			active = matchSSHHostPatterns(alias, directive.values)
			continue
		case "match":
			active = len(directive.values) == 1 && strings.EqualFold(directive.values[0], "all")
			continue
		}
		if !active || len(directive.values) == 0 {
			continue
		}
		value := directive.values[0]
		switch directive.key {
		case "hostname":
			if !hostSet {
				host.Host = value
				hostSet = true
			}
		case "port":
			if !portSet {
				if port, err := strconv.Atoi(value); err == nil {
					host.Port = port
					portSet = true
				}
			}
		case "user":
			if !userSet {
				host.User = value
				userSet = true
			}
		case "identityfile":
			if !keySet {
				if !strings.EqualFold(value, "none") {
					host.KeyPath = value
				}
				keySet = true
			}
		case "proxyjump":
			if !proxySet {
				if !strings.EqualFold(value, "none") {
					host.ProxyJump = value
				}
				proxySet = true
			}
		case "proxycommand":
			if !proxyCommandSet {
				host.ProxyCommand = !strings.EqualFold(value, "none")
				proxyCommandSet = true
			}
		}
	}

	host.Host = strings.ReplaceAll(host.Host, "%n", alias)
	host.Host = strings.ReplaceAll(host.Host, "%%", "%")
	host.User = strings.ReplaceAll(host.User, "%n", alias)
	if host.KeyPath != "" {
		replacer := strings.NewReplacer(
			"%%", "\x00",
			"%d", home,
			"%h", host.Host,
			"%n", alias,
			"%r", host.User,
			"%u", currentUser,
		)
		host.KeyPath = strings.ReplaceAll(replacer.Replace(host.KeyPath), "\x00", "%")
		if expanded, err := expandSSHPath(host.KeyPath, home); err == nil {
			host.KeyPath = expanded
		}
	}
	return host
}

func matchSSHHostPatterns(alias string, patterns []string) bool {
	matched := false
	for _, pattern := range patterns {
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if !matchSSHPattern(alias, pattern) {
			continue
		}
		if negated {
			return false
		}
		matched = true
	}
	return matched
}

func matchSSHPattern(value, pattern string) bool {
	var expression strings.Builder
	expression.WriteString("(?i)^")
	for _, char := range pattern {
		switch char {
		case '*':
			expression.WriteString(".*")
		case '?':
			expression.WriteByte('.')
		default:
			expression.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	expression.WriteByte('$')
	matched, err := regexp.MatchString(expression.String(), value)
	return err == nil && matched
}

func expandSSHInclude(pattern, includeBase string) (string, error) {
	if pattern == "~" || strings.HasPrefix(pattern, "~/") {
		home := filepath.Dir(includeBase)
		return expandSSHPath(pattern, home)
	}
	if filepath.IsAbs(pattern) {
		return filepath.Clean(pattern), nil
	}
	return filepath.Join(includeBase, pattern), nil
}

func expandSSHPath(path, home string) (string, error) {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func displaySSHPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if relative, relErr := filepath.Rel(home, path); relErr == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			if relative == "." {
				return "~"
			}
			return filepath.Join("~", relative)
		}
	}
	return path
}
