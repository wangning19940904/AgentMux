package framework

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/wangning19940904/AgentMux/internal/authflow"
	"github.com/wangning19940904/AgentMux/internal/traeauth"
)

// AuthAccount identifies who a framework CLI is signed in as. It carries only
// display fields; tokens, key material and raw command output never appear.
type AuthAccount struct {
	Email        string `json:"email,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Organization string `json:"organization,omitempty"`
	Plan         string `json:"plan,omitempty"`
	Method       string `json:"method,omitempty"`
	Region       string `json:"region,omitempty"`
}

const maxAccountFieldLength = 200

var cursorAccountPattern = regexp.MustCompile(`(?i)logged in as\s+(\S+)`)

// readFrameworkAccount extracts the signed-in account from the status output
// the CLI already produced, or from the credential file it owns. Failures are
// silent: account details are informational and never affect auth state.
func readFrameworkAccount(kind string, statusOutput []byte) *AuthAccount {
	var account AuthAccount
	switch kind {
	case "claudecode":
		account = claudeAccount(statusOutput)
	case "codex":
		account = codexAccount()
	case "cursor":
		account = cursorAccount(statusOutput)
	case "traecli":
		account = traeAccount()
	}
	return account.normalized()
}

func claudeAccount(output []byte) AuthAccount {
	var payload struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		Email            string `json:"email"`
		OrgName          string `json:"orgName"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(output, &payload) != nil || !payload.LoggedIn {
		return AuthAccount{}
	}
	return AuthAccount{
		Email:        payload.Email,
		Organization: payload.OrgName,
		Plan:         payload.SubscriptionType,
		Method:       payload.AuthMethod,
	}
}

func cursorAccount(output []byte) AuthAccount {
	match := cursorAccountPattern.FindStringSubmatch(authflow.StripControls(string(output)))
	if len(match) < 2 {
		return AuthAccount{}
	}
	identity := strings.TrimRight(match[1], ".,;:!)")
	if strings.Contains(identity, "@") {
		return AuthAccount{Email: identity}
	}
	return AuthAccount{UserID: identity}
}

// codexAccount reads the identity claims of the ID token Codex stored after a
// ChatGPT sign-in. The signature is not verified: the token is only displayed,
// never trusted for an authorization decision.
func codexAccount() AuthAccount {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return AuthAccount{}
		}
		home = filepath.Join(userHome, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return AuthAccount{}
	}
	var stored struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &stored) != nil {
		return AuthAccount{}
	}
	account := AuthAccount{Method: codexAuthMethod(stored.AuthMode)}
	claims, ok := decodeJWTClaims(stored.Tokens.IDToken)
	if !ok {
		return account
	}
	var identity struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Auth  struct {
			PlanType      string `json:"chatgpt_plan_type"`
			Organizations []struct {
				Title     string `json:"title"`
				IsDefault bool   `json:"is_default"`
			} `json:"organizations"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(claims, &identity) != nil {
		return account
	}
	account.Email = identity.Email
	account.Name = identity.Name
	account.Plan = identity.Auth.PlanType
	for _, org := range identity.Auth.Organizations {
		if org.IsDefault {
			account.Organization = org.Title
			break
		}
	}
	return account
}

func codexAuthMethod(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "chatgpt":
		return "ChatGPT"
	case "apikey", "api_key":
		return "API key"
	default:
		return mode
	}
}

func traeAccount() AuthAccount {
	meta, err := traeauth.ReadMetadata(nil)
	if err != nil {
		return AuthAccount{}
	}
	return AuthAccount{UserID: meta.UserID, Region: meta.Region, Method: meta.LoginMethod}
}

func decodeJWTClaims(token string) ([]byte, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	claims, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	return claims, true
}

// normalized strips control characters, bounds every field, and returns nil
// when nothing identifies the account so the API omits the object entirely.
func (a AuthAccount) normalized() *AuthAccount {
	clean := AuthAccount{
		Email:        cleanAccountField(a.Email),
		UserID:       cleanAccountField(a.UserID),
		Name:         cleanAccountField(a.Name),
		Organization: cleanAccountField(a.Organization),
		Plan:         cleanAccountField(a.Plan),
		Method:       cleanAccountField(a.Method),
		Region:       cleanAccountField(a.Region),
	}
	if clean == (AuthAccount{}) {
		return nil
	}
	return &clean
}

func cleanAccountField(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, authflow.StripControls(value)))
	if runes := []rune(value); len(runes) > maxAccountFieldLength {
		value = string(runes[:maxAccountFieldLength])
	}
	return value
}
