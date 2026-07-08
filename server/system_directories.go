package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type systemDirectoryRequest struct {
	Path string `json:"path"`
}

type systemDirectoryResponse struct {
	Path string `json:"path"`
}

func (s *Server) handleSystemDirectoryEnsure(w http.ResponseWriter, r *http.Request) {
	var req systemDirectoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	path, err := normalizeSystemDirectoryPath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is not a directory"})
		return
	}
	writeJSON(w, http.StatusOK, systemDirectoryResponse{Path: path})
}

func normalizeSystemDirectoryPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	if strings.HasPrefix(path, "~") {
		if path != "~" && !strings.HasPrefix(path, "~/") {
			return "", fmt.Errorf("only current-user home paths are supported")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	path = os.ExpandEnv(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
