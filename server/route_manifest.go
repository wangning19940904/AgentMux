package server

import "net/http"

type RouteStability string

const (
	RouteStable RouteStability = "stable"
	RouteBeta   RouteStability = "beta"
)

type PublicRouteSpec struct {
	Method    string
	Path      string
	Stability RouteStability
	Tenant    bool
}

// publicRouteManifest is the single inventory for the versioned public API.
// OpenAPI, tenant access policy, and mux registration are checked against it.
var publicRouteManifest = []PublicRouteSpec{
	{http.MethodGet, "/api/v1/capabilities", RouteStable, true},
	{http.MethodGet, "/api/v1/status", RouteStable, true},
	{http.MethodPost, "/api/v1/tenancy/register", RouteStable, false},
	{http.MethodGet, "/api/v1/tenancy/self", RouteStable, true},
	{http.MethodPost, "/api/v1/invocations", RouteStable, true},
	{http.MethodPost, "/api/v1/invocations/stream", RouteStable, true},
	{http.MethodPost, "/api/v1/send", RouteStable, true},
	{http.MethodGet, "/api/v1/agent-instances", RouteStable, true},
	{http.MethodPost, "/api/v1/agent-instances", RouteStable, true},
	{http.MethodDelete, "/api/v1/agent-instances", RouteStable, true},
	{http.MethodGet, "/api/v1/channels", RouteStable, true},
	{http.MethodPost, "/api/v1/channels", RouteStable, true},
	{http.MethodDelete, "/api/v1/channels", RouteStable, true},
	{http.MethodPost, consoleSessionEndpoint, RouteStable, true},
	{http.MethodGet, consoleEnterPath, RouteStable, false},
	{http.MethodGet, "/api/v1/orchestrations", RouteBeta, true},
	{http.MethodPost, "/api/v1/orchestrations", RouteBeta, true},
	{http.MethodPost, "/api/v1/orchestrations/cancel", RouteBeta, true},
	{http.MethodGet, "/api/v1/triggers", RouteBeta, true},
	{http.MethodPost, "/api/v1/triggers", RouteBeta, true},
	{http.MethodDelete, "/api/v1/triggers", RouteBeta, true},
	{http.MethodPost, "/api/v1/triggers/run", RouteBeta, true},
	{http.MethodGet, "/api/v1/usage", RouteBeta, true},
}

func publicTenantRoutePolicy() map[string][]string {
	policy := map[string][]string{}
	for _, route := range publicRouteManifest {
		if !route.Tenant {
			continue
		}
		policy[route.Path] = append(policy[route.Path], route.Method)
	}
	return policy
}
