package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/store"
)

const (
	redacted = "[REDACTED]"
	// maxString caps one string value: an error message is useful, a pasted
	// file inside one is not.
	maxString = 4000
	// minSecretLen is the shortest known secret replaced wherever it
	// appears. Shorter values ("1", "true") would garble unrelated text.
	minSecretLen = 6
)

var (
	// secretKey names a field whose value is a credential. It is matched
	// against the whole key, so "token_prefix" or "has_token" stay readable
	// but "bot_token" and "app_secret" do not.
	secretKey = regexp.MustCompile(`(?i)(^|[_\-.])(token|secret|password|passwd|passphrase|credential|credentials|cookie|cookies|authorization|api[_-]?key|apikey|private[_-]?key|access[_-]?key|encrypt[_-]?key|signing[_-]?key|master[_-]?key|session[_-]?key|client[_-]?secret|webhook|webhook[_-]?url|dsn|database[_-]?url|lease[_-]?secret)$`)
	// secretKeyLoose catches compound names the strict form misses, such as
	// "accessToken", "botToken" and "appSecret".
	secretKeyLoose = regexp.MustCompile(`(?i)(token|secret|password|passwd|apikey|privatekey|cookie)$`)
	// keyNotSecret are names that end like a credential but only describe
	// one (whether it is set, how long it lives).
	keyNotSecret = regexp.MustCompile(`(?i)(token_prefix|_tokens|tokens|token_count|has_[a-z_]+|[a-z_]+_set|[a-z_]+_configured|max_tokens|input_tokens|output_tokens|total_tokens|reasoning_tokens|tool_tokens|cache_[a-z_]*tokens)$`)
	// wholeMapKeys hold a map whose every value may be a credential: an
	// environment or a set of HTTP headers.
	wholeMapKeys = regexp.MustCompile(`(?i)^(env|environment|headers|http_headers|extra_headers)$`)
	// contentKey names a field that carries what someone wrote or an agent
	// replied. It is left out unless content was asked for.
	contentKey = regexp.MustCompile(`(?i)^(prompt|system_prompt|text|content|contents|body|message_text|messages|preview|last_message|last_message_preview|input|output|transcript|reply|response_text|summary|title|question|answer|arguments|result_text)$`)
	// secretEnvName picks environment variables whose values are collected
	// as known secrets.
	secretEnvName = regexp.MustCompile(`(?i)(token|secret|password|passwd|key|auth|credential|cookie|session|dsn|database_url)`)
	// urlUserinfo is the user:password@ part of a URL.
	urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s"'@]+@`)
	// inlineCredential is a cookie or credential header written inside a
	// sentence ("sent Cookie: abc"), which the line-anchored header pattern
	// of the observation redactor does not see.
	inlineCredential = regexp.MustCompile(`(?i)\b((?:set-)?cookie|authorization|proxy-authorization|x-api-key|sessionid|session_id|sid)(\s*[:=]\s*)(?:bearer\s+|basic\s+)?[^\s;,"'&]+`)
	// privateKeyBlock is a PEM private key pasted anywhere.
	privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// isSecretKey reports whether a field name holds a credential.
func isSecretKey(key string) bool {
	if keyNotSecret.MatchString(key) {
		return false
	}
	return secretKey.MatchString(key) || secretKeyLoose.MatchString(key)
}

// redactor turns collected values into bytes that are safe to hand to
// someone else: credentials removed, the home folder written as ~, message
// content left out unless asked for.
type redactor struct {
	homes          []string
	secrets        []string
	includeContent bool
}

func newRedactor(home string, includeContent bool) *redactor {
	r := &redactor{includeContent: includeContent}
	if home = strings.TrimRight(home, `/\`); home != "" && home != "/" {
		r.homes = append(r.homes, home)
		// macOS resolves some paths under /private; Windows paths reach
		// JSON with escaped backslashes.
		if strings.HasPrefix(home, "/") && !strings.HasPrefix(home, "/private/") {
			r.homes = append(r.homes, "/private"+home)
		}
		if strings.Contains(home, `\`) {
			r.homes = append(r.homes, strings.ReplaceAll(home, `\`, `\\`))
		}
	}
	return r
}

// addSecret records a literal value that must not appear in the bundle.
func (r *redactor) addSecret(value string) {
	value = strings.TrimSpace(value)
	if len(value) < minSecretLen || value == redacted {
		return
	}
	r.secrets = append(r.secrets, value)
	// A URL's password, and a value as it appears inside JSON.
	if strings.Contains(value, "://") {
		if m := urlUserinfo.FindString(value); m != "" {
			userinfo := strings.TrimSuffix(m[strings.Index(m, "://")+3:], "@")
			if _, password, ok := strings.Cut(userinfo, ":"); ok && len(password) >= minSecretLen {
				r.secrets = append(r.secrets, password)
			}
		}
	}
	if encoded, err := json.Marshal(value); err == nil {
		if inner := string(encoded[1 : len(encoded)-1]); inner != value {
			r.secrets = append(r.secrets, inner)
		}
	}
}

// addEnv records the values of secret-looking environment variables.
func (r *redactor) addEnv(environ []string) {
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok && secretEnvName.MatchString(name) {
			r.addSecret(value)
		}
	}
}

// collectSecrets walks a decoded value and records every credential in it:
// the values of secret fields, and every value of an env or headers map.
func (r *redactor) collectSecrets(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch {
			case isSecretKey(key):
				r.collectAll(child)
			case wholeMapKeys.MatchString(key):
				r.collectAll(child)
			default:
				r.collectSecrets(child)
			}
		}
	case []any:
		for _, child := range v {
			r.collectSecrets(child)
		}
	case string:
		if m := urlUserinfo.FindString(v); m != "" {
			r.addSecret(v)
		}
	}
}

func (r *redactor) collectAll(value any) {
	switch v := value.(type) {
	case string:
		r.addSecret(v)
	case map[string]any:
		for _, child := range v {
			r.collectAll(child)
		}
	case []any:
		for _, child := range v {
			r.collectAll(child)
		}
	}
}

// collectSecretsFrom records the credentials in any JSON-encodable value.
func (r *redactor) collectSecretsFrom(value any) {
	decoded, err := toGeneric(value)
	if err == nil {
		r.collectSecrets(decoded)
	}
}

// toGeneric round-trips a value through JSON into maps, slices and scalars.
func toGeneric(value any) (any, error) {
	var raw []byte
	switch v := value.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// scrub replaces credentials and, unless content is included, message
// content inside a decoded value.
func (r *redactor) scrub(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			switch {
			case isSecretKey(key):
				out[key] = maskValue(child)
			case wholeMapKeys.MatchString(key):
				out[key] = maskMapValues(child)
			case !r.includeContent && contentKey.MatchString(key):
				out[key] = omitContent(child)
			default:
				out[key] = r.scrub(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = r.scrub(child)
		}
		return out
	case string:
		return r.text(v)
	default:
		return v
	}
}

// maskValue hides a credential but says whether one was there.
func maskValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return ""
		}
		return redacted
	case bool, json.Number:
		return v
	default:
		return redacted
	}
}

// maskMapValues keeps an env or headers map's names and hides its values.
func maskMapValues(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return maskValue(value)
	}
	out := make(map[string]any, len(m))
	for key, child := range m {
		out[key] = maskValue(child)
	}
	return out
}

// omitContent replaces message content with its size.
func omitContent(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return ""
		}
		return fmt.Sprintf("[omitted: %d characters]", len([]rune(v)))
	case []any:
		return fmt.Sprintf("[omitted: %d items]", len(v))
	case map[string]any:
		return "[omitted]"
	default:
		return v
	}
}

// text cleans one free-text string: secret patterns, known secrets, URL
// passwords, private keys and the home folder.
func (r *redactor) text(s string) string {
	if s == "" {
		return s
	}
	s = privateKeyBlock.ReplaceAllString(s, redacted)
	s = urlUserinfo.ReplaceAllString(s, "${1}"+redacted+"@")
	s = inlineCredential.ReplaceAllString(s, "${1}${2}"+redacted)
	s = framework.RedactSecrets(s)
	if runes := []rune(s); len(runes) > maxString {
		s = string(runes[:maxString]) + "…[truncated]"
	}
	return s
}

// render encodes a value as indented JSON with everything removed that must
// not leave the machine.
func (r *redactor) render(value any) ([]byte, error) {
	generic, err := toGeneric(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(r.scrub(generic), "", "  ")
	if err != nil {
		return nil, err
	}
	return r.final(encoded), nil
}

// final is the last pass over a file's bytes, whatever produced them: the
// observation redactor's patterns, every known secret, then the home folder.
func (r *redactor) final(data []byte) []byte {
	secrets := append([]string(nil), r.secrets...)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	cleaned, _ := store.RedactObservationContent(data, secrets)
	// RedactObservationContent compacts JSON; indent it again for reading.
	if json.Valid(cleaned) {
		var indented bytes.Buffer
		if json.Indent(&indented, cleaned, "", "  ") == nil {
			cleaned = indented.Bytes()
		}
	}
	cleaned = privateKeyBlock.ReplaceAll(cleaned, []byte(redacted))
	for _, home := range r.homes {
		cleaned = replaceHome(cleaned, home)
	}
	if len(cleaned) > 0 && cleaned[len(cleaned)-1] != '\n' {
		cleaned = append(cleaned, '\n')
	}
	return cleaned
}

// replaceHome writes the home folder as ~ where it begins a path, so that
// /Users/al does not also turn /Users/alice into ~ice.
func replaceHome(data []byte, home string) []byte {
	needle := []byte(home)
	var out bytes.Buffer
	for {
		i := bytes.Index(data, needle)
		if i < 0 {
			out.Write(data)
			return out.Bytes()
		}
		end := i + len(needle)
		if end < len(data) && !isPathBoundary(data[end]) {
			out.Write(data[:end])
			data = data[end:]
			continue
		}
		out.Write(data[:i])
		out.WriteByte('~')
		data = data[end:]
	}
}

func isPathBoundary(b byte) bool {
	switch b {
	case '/', '\\', '"', '\'', ' ', '\n', '\r', '\t', ':', ';', ',', ')', ']', '}', '=':
		return true
	}
	return false
}
