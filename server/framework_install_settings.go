package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/wangning19940904/AgentMux/framework"
)

// Framework install settings let an administrator pick npm registries and
// replace the install/update command of catalogued CLIs on one machine.
// Because a custom command is arbitrary shell, the settings can only be
// changed from this machine's own Console (never through another machine's
// SSH proxy or fleet relay) and every custom command must be confirmed by the
// caller before it runs (see framework.InstallOptions).

const frameworkInstallSettingsKey = "framework_install_settings"

type frameworkInstallSettingsResponse struct {
	Settings   framework.InstallSettings           `json:"settings"`
	Frameworks []framework.InstallSettingsDefaults `json:"frameworks"`
}

// requestForwardedFromPeer reports whether another AgentMux relayed this
// request over SSH (the Console's remote proxy and fleet operations both mark
// forwarded requests with the originating host).
func requestForwardedFromPeer(r *http.Request) bool {
	return r.Header.Get("X-AgentMux-SSH-Host") != ""
}

// frameworkInstallSettings loads the stored settings. They were validated
// when saved; unreadable data falls back to the built-in commands.
func (s *Server) frameworkInstallSettings(ctx context.Context) framework.InstallSettings {
	settings := framework.InstallSettings{}
	if s.st == nil {
		return settings
	}
	raw, ok, err := s.st.GetSetting(ctx, frameworkInstallSettingsKey)
	if err != nil || !ok || raw == "" {
		if err != nil && s.log != nil {
			s.log.Warn("read framework install settings", "err", err)
		}
		return settings
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		if s.log != nil {
			s.log.Warn("framework install settings are unreadable; using built-in commands", "err", err)
		}
		return framework.InstallSettings{}
	}
	return settings
}

func plannedFrameworkCommands(status framework.Status, settings framework.InstallSettings) *frameworkCommands {
	spec := status.Spec
	if spec.KindType != framework.KindCLI || !spec.Supported {
		return nil
	}
	commands := &frameworkCommands{}
	if spec.InstallSupported && !status.Installed {
		if plan, err := framework.PlanCommand(spec, "install", settings); err == nil {
			commands.Install = &plan
		}
	}
	if spec.UpdateSupported && status.Installed {
		if plan, err := framework.PlanCommand(spec, "update", settings); err == nil {
			commands.Update = &plan
		}
	}
	if commands.Install == nil && commands.Update == nil {
		return nil
	}
	return commands
}

func (s *Server) handleFrameworkInstallSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, frameworkInstallSettingsResponse{
		Settings:   s.frameworkInstallSettings(r.Context()),
		Frameworks: framework.CustomizableFrameworks(),
	})
}

func (s *Server) handleFrameworkInstallSettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if requestForwardedFromPeer(r) {
		writeErr(w, http.StatusForbidden, "framework install settings can only be changed from this machine's own Console")
		return
	}
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "no store wired")
		return
	}
	var submitted framework.InstallSettings
	if !decodeJSONInto(w, r, &submitted) {
		return
	}
	settings, err := framework.NormalizeInstallSettings(submitted)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	blob, err := json.Marshal(settings)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetSetting(r.Context(), frameworkInstallSettingsKey, string(blob)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.log != nil {
		s.log.Info("framework install settings updated", "frameworks", len(settings.Frameworks), "default_registry", settings.DefaultRegistry != "")
	}
	writeJSON(w, http.StatusOK, frameworkInstallSettingsResponse{
		Settings:   settings,
		Frameworks: framework.CustomizableFrameworks(),
	})
}
