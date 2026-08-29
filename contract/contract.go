// Package contract pins the public integration contract version and hosts
// the golden schemas that keep Go types, openapi.yaml and the official SDKs
// from drifting apart. See CONTRACT.md for the versioning policy.
package contract

// Version is the public contract version returned by
// GET /api/v1/capabilities and GET /api/v1/status. It is independent from
// the binary version: bump minor for backward-compatible additions and major
// for breaking changes.
// 1.1 added tenancy: optional ownership fields on agent instances and
// channels, the /api/v1/tenancy/* endpoints, and the "tenancy" feature flag.
// 1.2 replaces enrollment codes with open tenant self-registration. New
// tenants start empty and receive resources only through administrator grants.
// 1.3 adds explicit Provider grants; tenant Provider catalogues and active
// routes are filtered to the Providers the administrator granted.
const Version = "1.3"
