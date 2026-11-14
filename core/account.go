package core

import (
	"fmt"
	"strings"
	"time"
)

// Accounts
//
// An account is a person who signs in to this AgentMux. An administrator
// account manages the whole control plane; a member account reaches only what
// it owns (its devices and direct messages, see Principal).
//
// How a person proves who they are is kept apart from the account: a session
// records the method that created it, and an account need not have a
// password. A single sign-on provider later only has to find or create the
// account and start a session for it.

// Account roles.
const (
	AccountRoleAdmin  = "admin"
	AccountRoleMember = "member"
)

// Account lifecycle states. A disabled account keeps what it owns but cannot
// sign in, and its sessions end.
const (
	AccountStatusActive   = "active"
	AccountStatusDisabled = "disabled"
)

// Sign-in methods recorded on a session.
const (
	SignInMethodPassword = "password"
)

// Account is one person. The password hash never leaves the store.
type Account struct {
	ID           string     `json:"id"`
	Login        string     `json:"login"`
	DisplayName  string     `json:"display_name,omitempty"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	HasPassword  bool       `json:"has_password"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastSignInAt *time.Time `json:"last_sign_in_at,omitempty"`
}

// Active reports whether the account may sign in.
func (a *Account) Active() bool {
	return a != nil && a.Status == AccountStatusActive
}

// IsAdmin reports whether the account administers this AgentMux.
func (a *Account) IsAdmin() bool {
	return a != nil && a.Role == AccountRoleAdmin
}

// Name is what the Console shows for the account.
func (a *Account) Name() string {
	if a == nil {
		return ""
	}
	if strings.TrimSpace(a.DisplayName) != "" {
		return a.DisplayName
	}
	return a.Login
}

// AccountIdentity is the part of an account a request carries.
type AccountIdentity struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

// Identity returns the request-facing view of an account.
func (a *Account) Identity() *AccountIdentity {
	if a == nil {
		return nil
	}
	return &AccountIdentity{ID: a.ID, Login: a.Login, DisplayName: a.DisplayName, Role: a.Role}
}

// AccountPrincipal is the principal for a signed-in person. An administrator
// sees the whole control plane; a member reaches only what they own.
func AccountPrincipal(account *Account) *Principal {
	if account == nil {
		return nil
	}
	if account.IsAdmin() {
		return &Principal{Admin: true, Account: account.Identity()}
	}
	return &Principal{Account: account.Identity()}
}

// NormalizeAccountRole maps arbitrary input to a role, defaulting to member
// so a malformed request never creates an administrator.
func NormalizeAccountRole(role string) string {
	if strings.TrimSpace(role) == AccountRoleAdmin {
		return AccountRoleAdmin
	}
	return AccountRoleMember
}

// NormalizeLogin lowercases and trims a login name.
func NormalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

// ValidateLogin accepts 2 to 64 lowercase letters, digits, '.', '_' and '-',
// starting with a letter or digit, so they stay plain in URLs and the CLI.
func ValidateLogin(login string) error {
	if len(login) < 2 || len(login) > 64 {
		return fmt.Errorf("a login name must be 2 to 64 characters")
	}
	for index, char := range login {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		case index > 0 && (char == '.' || char == '_' || char == '-'):
		default:
			return fmt.Errorf("a login name may use lowercase letters, digits, '.', '_' and '-', and must start with a letter or digit")
		}
	}
	return nil
}

// Password length bounds. The upper bound keeps hashing cost predictable.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 256
)

// ValidatePassword checks a new password's length.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("a password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("a password must be at most %d characters", MaxPasswordLength)
	}
	return nil
}
