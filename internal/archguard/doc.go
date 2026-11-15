// Package archguard holds only tests. They pin the shape of the codebase in
// hand-edited lists under testdata/, so that growth in the wrong direction
// fails a test and shows up as a changed line in a diff:
//
//   - boundaries_test.go: which packages may depend on which, with a
//     shrink-only allowlist of today's violations (boundary_allowed.txt).
//   - hub_surface_test.go: the routes the hub serves up the node link, the
//     calls its worker relay lets through and the tables it copies to a
//     standby (uplink_routes.txt, relay_allowed.txt, hub_tables.txt).
//   - server_size_test.go: line-count ceilings for the business logic that
//     still lives in server/ (server_size.txt).
//
// No list has an automatic rewrite: every change is a hand edit a reviewer
// sees. docs/architecture/guards.md explains each guard, how to read a
// failure and when adding a line is legitimate.
package archguard
