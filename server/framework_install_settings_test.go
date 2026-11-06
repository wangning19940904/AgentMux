package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

func doFrameworkSettingsRequest(t *testing.T, s *Server, method, path string, body any, configure func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if configure != nil {
		req = configure(req)
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

// isolateFrameworkDetection hides every real CLI on this machine so catalog
// entries report as not installed.
func isolateFrameworkDetection(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return bin
}

func TestFrameworkInstallSettingsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/frameworks/install-settings", framework.InstallSettings{
		DefaultRegistry: "https://bnpm.byted.org",
		Frameworks: map[string]framework.FrameworkOverride{
			"dsh":    {Registry: "https://registry.npmjs.org"},
			"gemini": {UpdateCommand: " pnpm add -g @google/gemini-cli "},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: code = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/frameworks/install-settings", nil)
	var got frameworkInstallSettingsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("get: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if got.Settings.DefaultRegistry != "https://bnpm.byted.org/" ||
		got.Settings.Frameworks["dsh"].Registry != "https://registry.npmjs.org/" ||
		got.Settings.Frameworks["gemini"].UpdateCommand != "pnpm add -g @google/gemini-cli" {
		t.Fatalf("stored settings = %+v", got.Settings)
	}
	defaults := map[string]framework.InstallSettingsDefaults{}
	for _, entry := range got.Frameworks {
		defaults[entry.Kind] = entry
	}
	if !defaults["dsh"].RegistryApplies || !strings.Contains(defaults["dsh"].InstallCommand, "--@deepseek-ai:registry=") {
		t.Fatalf("dsh defaults = %+v", defaults["dsh"])
	}
	if defaults["cursor"].RegistryApplies || defaults["cursor"].InstallCommand == "" {
		t.Fatalf("cursor defaults = %+v", defaults["cursor"])
	}
}

func TestFrameworkInstallSettingsRejectsUnsafeWrites(t *testing.T) {
	s, _ := newTestServer(t)
	valid := framework.InstallSettings{Frameworks: map[string]framework.FrameworkOverride{"gemini": {InstallCommand: "echo hi"}}}

	rec := doFrameworkSettingsRequest(t, s, http.MethodPut, "/api/v1/frameworks/install-settings", valid, func(r *http.Request) *http.Request {
		r.Header.Set("X-AgentMux-SSH-Host", "laptop")
		return r
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forwarded write: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = doFrameworkSettingsRequest(t, s, http.MethodPut, "/api/v1/frameworks/install-settings", valid, func(r *http.Request) *http.Request {
		return r.WithContext(withPrincipal(r.Context(), &core.Principal{TenantID: "tenant-1"}))
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant write: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/api/v1/frameworks/install-settings", framework.InstallSettings{DefaultRegistry: "ftp://mirror"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid registry: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := s.frameworkInstallSettings(t.Context()); len(got.Frameworks) != 0 || got.DefaultRegistry != "" {
		t.Fatalf("rejected writes must not be stored: %+v", got)
	}
}

func TestFrameworkInstallRunsCustomCommandOnlyAfterLocalConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := isolateFrameworkDetection(t)
	marker := filepath.Join(t.TempDir(), "ran")
	command := "touch '" + marker + "' && printf '#!/bin/sh\\necho gemini 9.9.9\\n' > '" + filepath.Join(bin, "gemini") + "' && chmod +x '" + filepath.Join(bin, "gemini") + "'"
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/frameworks/install-settings", framework.InstallSettings{
		Frameworks: map[string]framework.FrameworkOverride{"gemini": {InstallCommand: command}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: code = %d body = %s", rec.Code, rec.Body.String())
	}

	// The catalog shows the custom command so the Console can ask first.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/frameworks", nil)
	var list struct {
		Frameworks []frameworkView `json:"frameworks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var gemini *frameworkView
	for i := range list.Frameworks {
		if list.Frameworks[i].Spec.Kind == "gemini" {
			gemini = &list.Frameworks[i]
		}
	}
	if gemini == nil || gemini.Commands == nil || gemini.Commands.Install == nil || !gemini.Commands.Install.Custom || gemini.Commands.Install.Command != command {
		t.Fatalf("gemini catalog entry = %+v", gemini)
	}

	install := func(body map[string]any, forwarded bool) framework.InstallResult {
		t.Helper()
		rec := doFrameworkSettingsRequest(t, s, http.MethodPost, "/api/v1/frameworks/install", body, func(r *http.Request) *http.Request {
			if forwarded {
				r.Header.Set("X-AgentMux-SSH-Host", "laptop")
			}
			return r
		})
		var res framework.InstallResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
			t.Fatalf("install: code = %d body = %s", rec.Code, rec.Body.String())
		}
		return res
	}
	if res := install(map[string]any{"kind": "gemini"}, false); res.OK || !strings.Contains(res.Error, "confirmed") {
		t.Fatalf("unconfirmed install = %+v", res)
	}
	if res := install(map[string]any{"kind": "gemini", "confirmed_command": command}, true); res.OK || !strings.Contains(res.Error, "own Console") {
		t.Fatalf("forwarded install = %+v", res)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom command ran before local confirmation (stat err = %v)", err)
	}
	if res := install(map[string]any{"kind": "gemini", "confirmed_command": command}, false); !res.OK || res.Version != "9.9.9" {
		t.Fatalf("confirmed install = %+v", res)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("confirmed custom command did not run: %v", err)
	}
}
