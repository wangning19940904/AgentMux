package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Only run after SSH authentication and host-key verification. The wire data
// contains secrets: never include command output or parser errors in errors or
// logs. Use the existing TOML parser locally so old remote CLIs need no upgrade
// and remote machines need neither Python nor a TOML command-line utility.
const remoteTokenScript = `set -eu
exec 2>/dev/null
printf '\000AGENTMUX_AUTH_V1\000'
emit_token() {
  [ -z "$1" ] || printf 'token\000%s\000' "$1"
}
emit_config() {
  if [ -f "$1" ] && [ -r "$1" ]; then
    printf 'config\000'
    head -c 1048577 "$1"
    printf '\000'
  fi
}
emit_token "${AGENTMUX_BRIDGE_TOKEN-}"
case "$(uname -s)" in
  Darwin)
    for label in client server desktop; do
      plist="$HOME/Library/LaunchAgents/com.agentmux.$label.plist"
      if [ -f "$plist" ]; then
        token=$(/usr/libexec/PlistBuddy -c 'Print :EnvironmentVariables:AGENTMUX_BRIDGE_TOKEN' "$plist" || true)
        emit_token "$token"
      fi
    done
    default_config="$HOME/Library/Application Support/agentmux/config.toml"
    ;;
  *)
    if command -v systemctl >/dev/null 2>&1; then
      pid=$(systemctl --user show agentmux.service --property=MainPID --value || true)
      case "$pid" in
        ''|*[!0-9]*) ;;
        *)
          if [ -r "/proc/$pid/environ" ]; then
            token=$(tr '\000' '\n' < "/proc/$pid/environ" | sed -n 's/^AGENTMUX_BRIDGE_TOKEN=//p')
            emit_token "$token"
          fi
          ;;
      esac
    fi
    default_config="$HOME/.config/agentmux/config.toml"
    ;;
esac
if [ -n "${AMUX_CONFIG-}" ]; then
  case "$AMUX_CONFIG" in
    '~/'*) emit_config "$HOME/${AMUX_CONFIG#\~/}" ;;
    *) emit_config "$AMUX_CONFIG" ;;
  esac
else
  emit_config config.toml
  if [ -n "${XDG_CONFIG_HOME-}" ]; then
    emit_config "$XDG_CONFIG_HOME/agentmux/config.toml"
  else
    emit_config "$default_config"
  fi
  emit_config /etc/agentmux/config.toml
fi
printf 'end\000'
`

var remoteConfigEnvRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func readRemoteAPITokens(ctx context.Context, client remoteClient) ([]string, error) {
	output, err := client.Run(ctx, "sh -s", strings.NewReader(remoteTokenScript))
	if err != nil {
		// OpenSSH errors can themselves contain CombinedOutput, including tokens.
		return nil, errors.New("could not read remote AgentMux credentials over SSH")
	}
	marker := []byte("\x00AGENTMUX_AUTH_V1\x00")
	_, payload, found := bytes.Cut(output, marker)
	if !found || len(payload) > 4<<20 {
		return nil, errors.New("invalid remote AgentMux credential response")
	}
	fields := strings.Split(string(payload), "\x00")
	var tokens []string
	seen := map[string]bool{}
	for len(fields) >= 2 {
		kind := fields[0]
		if kind == "end" {
			return tokens, nil
		}
		value := fields[1]
		fields = fields[2:]
		if len(value) > 1<<20 {
			return nil, errors.New("remote AgentMux credential source is too large")
		}
		switch kind {
		case "token":
		case "config":
			value, err = remoteConfigToken(ctx, client, value)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("invalid remote AgentMux credential response")
		}
		value = strings.TrimSpace(value)
		if value != "" && !strings.ContainsAny(value, "\r\n\x00") && len(value) <= 65536 && !seen[value] {
			seen[value] = true
			tokens = append(tokens, value)
		}
	}
	return nil, errors.New("incomplete remote AgentMux credential response")
}

func remoteConfigToken(ctx context.Context, client remoteClient, raw string) (string, error) {
	// Match config.Load's expansion before TOML decoding, but resolve variables
	// on the remote machine, never from the controller's own environment.
	values := map[string]string{}
	var names []string
	var script strings.Builder
	script.WriteString(`printf '\000AGENTMUX_ENV_V1\000'` + "\n")
	for _, match := range remoteConfigEnvRE.FindAllStringSubmatch(raw, -1) {
		name := match[1]
		if _, ok := values[name]; ok {
			continue
		}
		if len(values) >= 64 {
			return "", errors.New("too many remote AgentMux config environment references")
		}
		values[name] = ""
		names = append(names, name)
		// Names are restricted by the regexp; no config text is evaluated as shell.
		script.WriteString(`printf '%s\000' "${` + name + `-}"` + "\n")
	}
	if len(names) > 0 {
		out, err := client.Run(ctx, "sh -s", strings.NewReader(script.String()))
		_, payload, found := bytes.Cut(out, []byte("\x00AGENTMUX_ENV_V1\x00"))
		if err != nil || !found || len(payload) > 1<<20 {
			return "", errors.New("could not resolve remote AgentMux config environment")
		}
		fields := strings.Split(string(payload), "\x00")
		if len(fields) != len(names)+1 {
			return "", errors.New("invalid remote AgentMux config environment response")
		}
		for i, name := range names {
			values[name] = fields[i]
		}
	}
	raw = remoteConfigEnvRE.ReplaceAllStringFunc(raw, func(match string) string {
		return values[remoteConfigEnvRE.FindStringSubmatch(match)[1]]
	})
	var cfg struct {
		Bridge struct {
			Token string `toml:"token"`
		} `toml:"bridge"`
	}
	if _, err := toml.Decode(raw, &cfg); err != nil {
		return "", errors.New("could not parse remote AgentMux config for API authentication")
	}
	return cfg.Bridge.Token, nil
}

func recoverRemoteAPIToken(ctx context.Context, client remoteClient, host Host) (Host, map[string]any, error) {
	tokens, err := readRemoteAPITokens(ctx, client)
	if err != nil {
		return host, nil, err
	}
	for _, token := range tokens {
		if token == host.APIToken {
			continue
		}
		candidate := host
		candidate.APIToken = token
		status, err := requestStatus(ctx, client, candidate)
		if err == nil {
			return candidate, status, nil
		}
		if ctx.Err() != nil {
			return host, nil, ctx.Err()
		}
		var response *remoteHTTPError
		if errors.As(err, &response) {
			if response.status == http.StatusUnauthorized {
				continue
			}
			// A remote response may echo the credential; return only its status.
			return host, nil, fmt.Errorf("remote AgentMux returned HTTP %d while verifying its API token", response.status)
		}
		return host, nil, errors.New("could not verify remote AgentMux API token over SSH")
	}
	return host, nil, errors.New("remote AgentMux returned HTTP 401: no usable API token found over SSH; check the remote AgentMux config or set Remote API Token")
}
