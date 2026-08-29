package core

import "time"

// Tenancy model
//
// AgentMux is one control plane shared by several host applications: a Web UI,
// a desktop app, a backend service. Each of those is a Tenant. Resources a
// tenant creates are owned by it and invisible to its peers; the admin
// principal (the config.toml bridge token, and Console sessions minted by it)
// sees every resource together with its owner.
//
// A tenant additionally reaches resources that are marked public, and
// resources an admin granted to it explicitly (see ResourceGrant).

// Tenant kinds. Purely descriptive: they drive Console labels, not policy.
const (
	TenantKindApp     = "app"
	TenantKindWeb     = "web"
	TenantKindService = "service"
)

// Tenant lifecycle states. A disabled tenant keeps its resources but its
// tokens stop authenticating.
const (
	TenantStatusActive   = "active"
	TenantStatusDisabled = "disabled"
)

// Resource visibility. Private is the default for anything a tenant creates;
// public makes a resource readable and usable by every tenant.
const (
	VisibilityPrivate = "private"
	VisibilityPublic  = "public"
)

// Grant levels, ordered from least to most privileged.
//
//	read   — the resource is listed and readable
//	use    — read, plus the tenant may invoke or send through it
//	manage — use, plus the tenant may modify and delete it
const (
	GrantLevelRead   = "read"
	GrantLevelUse    = "use"
	GrantLevelManage = "manage"
)

// Grantable resource types.
const (
	ResourceTypeAgent    = "agent"
	ResourceTypeChannel  = "channel"
	ResourceTypeTrigger  = "trigger"
	ResourceTypeProvider = "provider"
	// Orchestrations remain tenant-scoped runtime records, but are no longer
	// offered as administrator-granted catalogue resources.
	ResourceTypeOrchestration = "orchestration"
)

// grantRank orders the levels so a required level can be compared against a
// held one without a lookup table at every call site.
var grantRank = map[string]int{
	GrantLevelRead:   1,
	GrantLevelUse:    2,
	GrantLevelManage: 3,
}

// GrantSatisfies reports whether a held grant level covers the required one.
// An unknown or empty held level satisfies nothing.
func GrantSatisfies(held, required string) bool {
	heldRank, ok := grantRank[held]
	if !ok {
		return false
	}
	requiredRank, ok := grantRank[required]
	if !ok {
		return false
	}
	return heldRank >= requiredRank
}

// StrongerGrant returns whichever of two levels grants more. An empty or
// unrecognised value ranks below every real level, which lets callers fold a
// list of candidate levels together starting from "no access".
func StrongerGrant(a, b string) string {
	if grantRank[a] >= grantRank[b] {
		return a
	}
	return b
}

// NormalizeGrantLevel maps an arbitrary input to a known level, defaulting to
// read so a malformed request can never widen access.
func NormalizeGrantLevel(level string) string {
	if _, ok := grantRank[level]; ok {
		return level
	}
	return GrantLevelRead
}

// NormalizeVisibility maps an arbitrary input to a known visibility,
// defaulting to private.
func NormalizeVisibility(visibility string) string {
	if visibility == VisibilityPublic {
		return VisibilityPublic
	}
	return VisibilityPrivate
}

// Tenant is one registered host application.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind,omitempty"`
	Status    string    `json:"status"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Active reports whether the tenant may authenticate.
func (t *Tenant) Active() bool {
	return t != nil && t.Status == TenantStatusActive
}

// TenantToken is a credential belonging to a tenant. Only the SHA-256 hash is
// persisted; the plaintext is returned once at creation and never again, so a
// leaked database cannot be replayed against the API.
type TenantToken struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name,omitempty"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Secret carries the plaintext token on the create response only.
	Secret string `json:"secret,omitempty"`
}

// ResourceGrant gives one tenant access to a resource it does not own.
type ResourceGrant struct {
	TenantID     string    `json:"tenant_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Level        string    `json:"level"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Principal is the authenticated identity behind a request. Exactly one of
// Admin or Tenant is meaningful: an admin principal bypasses every ownership
// check, a tenant principal is confined to its visible set.
type Principal struct {
	Admin      bool
	TenantID   string
	TenantName string
}

// IsTenant reports whether the principal is a scoped tenant.
func (p *Principal) IsTenant() bool {
	return p != nil && !p.Admin && p.TenantID != ""
}

// AdminPrincipal is the unscoped principal used by the bridge token, the CLI
// and any internal caller that legitimately needs the whole control plane.
func AdminPrincipal() *Principal {
	return &Principal{Admin: true}
}
