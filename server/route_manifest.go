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
}

// publicRouteManifest is the single inventory for the versioned public API.
// OpenAPI and mux registration are checked against it.
var publicRouteManifest = []PublicRouteSpec{
	{http.MethodGet, "/api/v1/event-sources", RouteBeta},
	{http.MethodGet, "/api/v1/event-subscriptions", RouteBeta},
	{http.MethodPost, "/api/v1/event-subscriptions", RouteBeta},
	{http.MethodDelete, "/api/v1/event-subscriptions", RouteBeta},
	{http.MethodPost, "/api/v1/event-subscriptions/test", RouteBeta},
	{http.MethodPost, "/api/v1/event-subscriptions/rotate-secret", RouteBeta},
	{http.MethodGet, "/api/v1/event-deliveries", RouteBeta},
	{http.MethodPost, "/api/v1/event-deliveries/retry", RouteBeta},

	{http.MethodGet, "/api/v1/capabilities", RouteStable},
	{http.MethodGet, "/api/v1/status", RouteStable},
	{http.MethodPost, "/api/v1/invocations", RouteStable},
	{http.MethodPost, "/api/v1/invocations/stream", RouteStable},
	{http.MethodPost, "/api/v1/send", RouteStable},
	{http.MethodGet, "/api/v1/agent-instances", RouteStable},
	{http.MethodPost, "/api/v1/agent-instances", RouteStable},
	{http.MethodDelete, "/api/v1/agent-instances", RouteStable},
	{http.MethodGet, "/api/v1/channels", RouteStable},
	{http.MethodPost, "/api/v1/channels", RouteStable},
	{http.MethodDelete, "/api/v1/channels", RouteStable},
	{http.MethodPost, consoleSessionEndpoint, RouteStable},
	{http.MethodGet, consoleEnterPath, RouteStable},
	{http.MethodGet, "/api/v1/orchestrations", RouteBeta},
	{http.MethodPost, "/api/v1/orchestrations", RouteBeta},
	{http.MethodPost, "/api/v1/orchestrations/cancel", RouteBeta},
	{http.MethodGet, "/api/v1/triggers", RouteBeta},
	{http.MethodPost, "/api/v1/triggers", RouteBeta},
	{http.MethodDelete, "/api/v1/triggers", RouteBeta},
	{http.MethodPost, "/api/v1/triggers/run", RouteBeta},
	{http.MethodGet, "/api/v1/usage", RouteBeta},
}
