package tts

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Progress is emitted while the runtime or a model is downloaded and unpacked.
type Progress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Detail  string `json:"detail,omitempty"`
}

// ModelStatus combines catalog metadata with local installation state.
type ModelStatus struct {
	Model
	Installed   bool `json:"installed"`
	Downloading bool `json:"downloading,omitempty"`
}

// RuntimeStatus describes the shared local inference executable.
type RuntimeStatus struct {
	Version       string `json:"version"`
	Supported     bool   `json:"supported"`
	Installed     bool   `json:"installed"`
	DownloadBytes int64  `json:"download_bytes,omitempty"`
	Platform      string `json:"platform"`
}

// CatalogStatus is returned to the App's local-model picker.
type CatalogStatus struct {
	Models  []ModelStatus `json:"models"`
	Runtime RuntimeStatus `json:"runtime"`
}

// Manager owns files below ~/.agentmux/tts and serializes per-model installs.
type Manager struct {
	root   string
	log    *slog.Logger
	client *http.Client
	mu     sync.Mutex
	active map[string]bool
}

// DefaultRoot returns the model storage directory. AGENTMUX_TTS_DIR is useful
// for portable installs and tests.
func DefaultRoot() string {
	if value := strings.TrimSpace(os.Getenv("AGENTMUX_TTS_DIR")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".agentmux", "tts")
	}
	return filepath.Join(home, ".agentmux", "tts")
}

func NewManager(root string, log *slog.Logger) *Manager {
	if strings.TrimSpace(root) == "" {
		root = DefaultRoot()
	}
	return &Manager{
		root: root, log: log,
		client: &http.Client{Timeout: 30 * time.Minute},
		active: map[string]bool{},
	}
}

func (m *Manager) Catalog() CatalogStatus {
	asset, supported := currentRuntimeAsset()
	m.mu.Lock()
	active := make(map[string]bool, len(m.active))
	for id, value := range m.active {
		active[id] = value
	}
	m.mu.Unlock()
	models := make([]ModelStatus, 0, len(modelCatalog))
	for _, spec := range modelCatalog {
		models = append(models, ModelStatus{Model: spec.Model, Installed: m.modelInstalled(spec), Downloading: active[spec.ID]})
	}
	return CatalogStatus{
		Models: models,
		Runtime: RuntimeStatus{
			Version: runtimeVersion, Supported: supported, Installed: supported && m.runtimeInstalled(asset),
			DownloadBytes: asset.bytes, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		},
	}
}

func (m *Manager) IsInstalled(id string) bool {
	spec, ok := lookupSpec(id)
	return ok && m.modelInstalled(spec)
}

func (m *Manager) Install(ctx context.Context, id string, report func(Progress)) (ModelStatus, error) {
	spec, ok := lookupSpec(strings.TrimSpace(id))
	if !ok {
		return ModelStatus{}, fmt.Errorf("unknown local TTS model %q", id)
	}
	asset, supported := currentRuntimeAsset()
	if !supported {
		return ModelStatus{}, fmt.Errorf("local TTS is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	m.mu.Lock()
	if m.active[spec.ID] {
		m.mu.Unlock()
		return ModelStatus{}, fmt.Errorf("model %q is already downloading", spec.ID)
	}
	m.active[spec.ID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.active, spec.ID)
		m.mu.Unlock()
	}()
	emit(report, "prepare", 1, "Preparing local TTS storage")
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return ModelStatus{}, fmt.Errorf("create local TTS directory: %w", err)
	}
	if err := m.ensureRuntime(ctx, asset, func(p Progress) {
		p.Percent = p.Percent / 5
		emit(report, p.Phase, p.Percent, p.Detail)
	}); err != nil {
		return ModelStatus{}, err
	}
	emit(report, "download", 20, "Downloading "+spec.Name)
	archive, err := m.download(ctx, spec.archiveURL, spec.DownloadBytes, spec.sha256, func(done, total int64) {
		percent := 20
		if total > 0 {
			percent += int(float64(done) / float64(total) * 68)
		}
		emit(report, "download", percent, fmt.Sprintf("%s / %s", formatBytes(done), formatBytes(total)))
	})
	if err != nil {
		return ModelStatus{}, fmt.Errorf("download %s: %w", spec.Name, err)
	}
	defer os.Remove(archive)
	emit(report, "extract", 90, "Installing model files")
	if err := m.installArchive(archive, m.modelDir(spec.ID), spec.archiveRoot, modelManifest{ID: spec.ID, InstalledAt: time.Now()}); err != nil {
		return ModelStatus{}, fmt.Errorf("install %s: %w", spec.Name, err)
	}
	if !m.modelInstalled(spec) {
		return ModelStatus{}, fmt.Errorf("installed model %q is incomplete", spec.ID)
	}
	emit(report, "complete", 100, "Model is ready")
	return ModelStatus{Model: spec.Model, Installed: true}, nil
}

// Remove deletes one selected model. The shared runtime is retained for other
// models and a removed model can always be downloaded again.
func (m *Manager) Remove(id string) error {
	spec, ok := lookupSpec(strings.TrimSpace(id))
	if !ok {
		return fmt.Errorf("unknown local TTS model %q", id)
	}
	m.mu.Lock()
	busy := m.active[spec.ID]
	m.mu.Unlock()
	if busy {
		return fmt.Errorf("model %q is still downloading", spec.ID)
	}
	return os.RemoveAll(m.modelDir(spec.ID))

}

type modelManifest struct {
	ID          string    `json:"id"`
	InstalledAt time.Time `json:"installed_at"`
}

func (m *Manager) ensureRuntime(ctx context.Context, asset runtimeAsset, report func(Progress)) error {
	if m.runtimeInstalled(asset) {
		emit(report, "runtime", 100, "Local TTS runtime is ready")
		return nil
	}
	emit(report, "runtime", 2, "Downloading local TTS runtime")
	file, err := m.download(ctx, asset.url, asset.bytes, asset.sha256, func(done, total int64) {
		percent := 5
		if total > 0 {
			percent += int(float64(done) / float64(total) * 80)
		}
		emit(report, "runtime", percent, fmt.Sprintf("%s / %s", formatBytes(done), formatBytes(total)))
	})
	if err != nil {
		return fmt.Errorf("download local TTS runtime: %w", err)
	}
	defer os.Remove(file)
	target := m.runtimeDir()
	if asset.archive {
		if err := m.installArchive(file, target, "", nil); err != nil {
			return fmt.Errorf("install local TTS runtime: %w", err)
		}
	} else {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := copyFile(file, filepath.Join(target, "bin", asset.binaryName), 0o755); err != nil {
			return fmt.Errorf("install local TTS executable: %w", err)
		}
	}
	if !m.runtimeInstalled(asset) {
		return errors.New("local TTS runtime installation is incomplete")
	}
	emit(report, "runtime", 100, "Local TTS runtime is ready")
	return nil

}

func (m *Manager) download(ctx context.Context, rawURL string, expected int64, expectedSHA string, progress func(int64, int64)) (string, error) {
	downloadDir := filepath.Join(m.root, ".downloads")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(downloadDir, "download-*.part")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AgentMux local TTS manager")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	total := resp.ContentLength
	if expected > 0 {
		total = expected
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), &progressReader{reader: resp.Body, total: total, progress: progress})
	if err != nil {
		return "", err
	}
	if expected > 0 && written != expected {
		return "", fmt.Errorf("downloaded %d bytes; expected %d", written, expected)
	}
	if expectedSHA != "" && hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(expectedSHA, "sha256:") {
		return "", errors.New("download checksum mismatch")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil

}

type progressReader struct {
	reader   io.Reader
	total    int64
	done     int64
	progress func(int64, int64)
	last     time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.done += int64(n)
	if r.progress != nil && (time.Since(r.last) >= 150*time.Millisecond || err != nil) {
		r.progress(r.done, r.total)
		r.last = time.Now()
	}
	return n, err
}

func (m *Manager) installArchive(archivePath, target, expectedRoot string, manifest any) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".tts-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractTarBzip2(archivePath, staging); err != nil {
		return err
	}
	source := staging
	if expectedRoot != "" {
		source = filepath.Join(staging, expectedRoot)
	} else if entries, readErr := os.ReadDir(staging); readErr == nil && len(entries) == 1 && entries[0].IsDir() {
		source = filepath.Join(staging, entries[0].Name())
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return fmt.Errorf("archive does not contain expected directory %q", expectedRoot)
	}
	if manifest != nil {
		body, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(source, ".agentmux-model.json"), body, 0o644); err != nil {
			return err
		}
	}
	return replaceDirectory(source, target)
}

func extractTarBzip2(archivePath, target string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(bzip2.NewReader(file))
	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := archiveTarget(cleanTarget, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, header.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry %q", header.Name)
		}
	}
}

func archiveTarget(root, name string) (string, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if cleanName == "." || filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	path := filepath.Join(cleanRoot, cleanName)
	if path == cleanRoot || !strings.HasPrefix(path, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return path, nil
}

func replaceDirectory(source, target string) error {
	backup := target + ".previous"
	_ = os.RemoveAll(backup)
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
		hadTarget = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if hadTarget {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func copyFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func (m *Manager) modelInstalled(spec modelSpec) bool {
	dir := m.modelDir(spec.ID)
	for _, name := range spec.required {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func (m *Manager) runtimeInstalled(asset runtimeAsset) bool {
	if asset.binaryName == "" {
		return false
	}
	info, err := os.Stat(m.runtimeBinary(asset))
	return err == nil && !info.IsDir()
}

func (m *Manager) modelDir(id string) string { return filepath.Join(m.root, "models", id) }
func (m *Manager) runtimeDir() string        { return filepath.Join(m.root, "runtime", runtimeVersion) }
func (m *Manager) runtimeBinary(asset runtimeAsset) string {
	return filepath.Join(m.runtimeDir(), "bin", asset.binaryName)
}

func emit(report func(Progress), phase string, percent int, detail string) {
	if report == nil {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	report(Progress{Phase: phase, Percent: percent, Detail: detail})
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "unknown"
	}
	const mb = 1024 * 1024
	if value >= mb {
		return fmt.Sprintf("%.1f MB", float64(value)/mb)
	}
	return fmt.Sprintf("%.1f KB", float64(value)/1024)
}
