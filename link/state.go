package link

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	joinTokenPrefix = "amxj_"
	stateFileName   = "hub-link.json"
)

// JoinToken is everything a node needs to pair: where the hub is, a one-time
// code, and the hub certificate's fingerprint when the hub uses its own.
type JoinToken struct {
	HubURL      string `json:"u"`
	Code        string `json:"c"`
	Fingerprint string `json:"f,omitempty"`
}

func (t JoinToken) String() string {
	data, _ := json.Marshal(t)
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseJoinToken reads a token printed by the hub.
func ParseJoinToken(value string) (JoinToken, error) {
	value = strings.TrimSpace(value)
	encoded, ok := strings.CutPrefix(value, joinTokenPrefix)
	if !ok {
		return JoinToken{}, errors.New("not a join token: it should start with " + joinTokenPrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return JoinToken{}, errors.New("the join token is damaged; copy it again from the hub")
	}
	var token JoinToken
	if err := json.Unmarshal(data, &token); err != nil {
		return JoinToken{}, errors.New("the join token is damaged; copy it again from the hub")
	}
	token.HubURL, err = NormalizeHubURL(token.HubURL)
	if err != nil {
		return JoinToken{}, err
	}
	if strings.TrimSpace(token.Code) == "" {
		return JoinToken{}, errors.New("the join token has no pairing code")
	}
	return token, nil
}

// NormalizeHubURL accepts http and https addresses of a hub, without a path.
func NormalizeHubURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("hub address %q must look like https://host:port", value)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("hub address %q must start with http:// or https://", value)
	}
	return parsed.Scheme + "://" + parsed.Host + strings.TrimSuffix(parsed.Path, "/"), nil
}

// State is a node's membership of one hub. Token is the device's credential;
// the file holding it is readable by its owner only.
type State struct {
	HubURL      string    `json:"hub_url"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	DeviceID    string    `json:"device_id"`
	Token       string    `json:"token"`
	Name        string    `json:"name,omitempty"`
	JoinedAt    time.Time `json:"joined_at"`
}

// StateDir is where the link's files live: dir when it is set, otherwise the
// user's AgentMux configuration directory.
func StateDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "agentmux"), nil
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	return filepath.Abs(dir)
}

// StatePath is where a node keeps its hub membership.
func StatePath(dir string) (string, error) {
	dir, err := StateDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateFileName), nil
}

// LoadState reads the membership file. A missing file is not an error: the
// node has simply not joined a hub.
func LoadState(path string) (State, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	if state.HubURL == "" || state.Token == "" {
		return State{}, false, fmt.Errorf("%s is incomplete; run `amux join` again", path)
	}
	return state, true, nil
}

// SaveState replaces the membership file atomically.
func SaveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

// RemoveState forgets the hub. Removing a file that is not there succeeds.
func RemoveState(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
