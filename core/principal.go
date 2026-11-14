package core

// Principal is the authenticated identity behind a request.
//
// AgentMux has one administrator scope and, when people sign in, member
// accounts:
//
//   - Admin: the config.toml bridge token, the loopback Console of a machine
//     without a bridge token, and administrator accounts. It sees and manages
//     the whole control plane.
//   - Member: a signed-in account without the administrator role. A member
//     reaches only what is theirs: the devices they paired, their direct
//     messages and their own password (see the member route policy in the
//     server). Everything else is reserved for administrators.
//
// Account is set when a person signed in, administrator or member.
type Principal struct {
	Admin   bool
	Account *AccountIdentity
}

// IsMember reports whether the principal is a signed-in member account.
func (p *Principal) IsMember() bool {
	return p != nil && !p.Admin && p.Account != nil
}

// OwnerAccountID is the account that should own what this principal creates
// on its own behalf, such as a device it pairs. It is empty for the bridge
// token and other callers that are not a person, which leaves the resource
// unassigned and visible to administrators only.
func (p *Principal) OwnerAccountID() string {
	if p == nil || p.Account == nil {
		return ""
	}
	return p.Account.ID
}

// AdminPrincipal is the unscoped principal used by the bridge token, the CLI
// and any internal caller that legitimately needs the whole control plane.
func AdminPrincipal() *Principal {
	return &Principal{Admin: true}
}
