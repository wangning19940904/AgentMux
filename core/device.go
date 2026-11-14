package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// DeviceInfo is what a node says about itself when it pairs with a hub and
// each time it connects.
type DeviceInfo struct {
	Name     string `json:"name,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Version  string `json:"version,omitempty"`
}

// Device is a node paired with this hub. The node opens the connection, so the
// hub needs no address for it. Only the hash of its token is stored.
type Device struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Hostname    string     `json:"hostname,omitempty"`
	OS          string     `json:"os,omitempty"`
	Arch        string     `json:"arch,omitempty"`
	Version     string     `json:"version,omitempty"`
	TokenPrefix string     `json:"token_prefix,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	// OwnerAccountID is the account that created the device's pairing code.
	// Empty means it was paired with administrator credentials and only
	// administrators see it.
	OwnerAccountID   string `json:"owner_account_id,omitempty"`
	OwnerAccountName string `json:"owner_account_name,omitempty"`
}

// DevicePairingCode lets one node pair with the hub once. Code carries the
// plaintext on the create response only.
type DevicePairingCode struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Code      string    `json:"code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Device report shapes. A list is a JSON array of objects, kept one document
// per element so a change to one element is one small write; anything else is
// a single value.
const (
	DeviceReportList  = "list"
	DeviceReportValue = "value"
)

// DeviceReportDoc is one document of a collection a device reports.
type DeviceReportDoc struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// DeviceReport is a change to one collection of one device: the answer its
// own API gives at Path, sent as what changed since the hub's copy. Order
// lists every document id when the order changed; nil leaves it as it was.
type DeviceReport struct {
	Collection string            `json:"-"`
	Path       string            `json:"path"`
	Shape      string            `json:"shape"`
	Order      []string          `json:"order,omitempty"`
	Upserts    []DeviceReportDoc `json:"upserts,omitempty"`
	Deletes    []string          `json:"deletes,omitempty"`
}

// DeviceCollection summarizes what the hub holds of one collection.
type DeviceCollection struct {
	DeviceID   string    `json:"device_id"`
	Collection string    `json:"collection"`
	Path       string    `json:"path"`
	Shape      string    `json:"shape"`
	Items      int       `json:"items"`
	ReportedAt time.Time `json:"reported_at"`
}

// DeviceReportHash identifies a document's content. Device and hub compute
// it the same way, so the hub never has to trust a hash a device sends.
func DeviceReportHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
