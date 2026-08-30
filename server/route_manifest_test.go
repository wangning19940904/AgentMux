package server

import (
	"bufio"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPublicRouteManifestMatchesOpenAPIAndMux(t *testing.T) {
	openAPI, err := os.Open("../contract/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer openAPI.Close()
	var documented []string
	path := ""
	scanner := bufio.NewScanner(openAPI)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		trimmed := strings.TrimSpace(line)
		if path != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") {
			switch trimmed {
			case "get:", "post:", "put:", "delete:", "patch:":
				documented = append(documented, strings.ToUpper(strings.TrimSuffix(trimmed, ":"))+" "+path)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	manifest := make([]string, 0, len(publicRouteManifest))
	server, _ := newTestServer(t)
	for _, route := range publicRouteManifest {
		manifest = append(manifest, route.Method+" "+route.Path)
		requestPath := route.Path
		request, _ := httpRequest(route.Method, requestPath)
		_, pattern := server.mux.Handler(request)
		if pattern == "" {
			t.Errorf("manifest route is not registered: %s %s", route.Method, route.Path)
		}
		if route.Tenant && strings.HasPrefix(route.Path, "/api/") && !tenantRouteAllowed(route.Method, route.Path) {
			t.Errorf("manifest tenant route is denied: %s %s", route.Method, route.Path)
		}
	}
	sort.Strings(documented)
	sort.Strings(manifest)
	if !reflect.DeepEqual(documented, manifest) {
		t.Fatalf("public route drift\nOpenAPI: %v\nmanifest: %v", documented, manifest)
	}
}

func httpRequest(method, path string) (*http.Request, error) {
	return http.NewRequest(method, "http://127.0.0.1"+path, nil)
}
