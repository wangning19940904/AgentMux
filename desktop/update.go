//go:build desktop

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopLatestReleaseAPI = "https://api.github.com/repos/wangning19940904/AgentMux/releases/latest"
	maxDesktopUpdateBytes   = int64(512 << 20)
)

var desktopUpdateHTTPClient = &http.Client{Timeout: 90 * time.Second}

// DesktopUpdateStatus is the native update state shown under System > Settings.
type DesktopUpdateStatus struct {
	Supported       bool   `json:"supported"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	Restarting      bool   `json:"restarting,omitempty"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type desktopRelease struct {
	status      DesktopUpdateStatus
	downloadURL string
	checksumURL string
}

// CheckDesktopUpdate compares this build with the latest published desktop
// release. Development binaries can check, but only a packaged .app can install.
func (a *App) CheckDesktopUpdate() (DesktopUpdateStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := checkDesktopRelease(ctx, desktopUpdateHTTPClient, desktopLatestReleaseAPI, version, packagedDesktopApp())
	return release.status, err
}

// InstallDesktopUpdate downloads the architecture-matched .app, verifies its
// published SHA-256 checksum and signature, atomically swaps the bundle, then
// restarts AgentMux. The previous bundle remains in place until relaunch starts.
func (a *App) InstallDesktopUpdate() (DesktopUpdateStatus, error) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()

	ctx := a.ctx
	if ctx == nil {
		return DesktopUpdateStatus{}, errors.New("desktop app is not ready")
	}
	release, err := checkDesktopRelease(ctx, desktopUpdateHTTPClient, desktopLatestReleaseAPI, version, packagedDesktopApp())
	if err != nil {
		return release.status, err
	}
	if !release.status.UpdateAvailable {
		return release.status, nil
	}
	if !release.status.Supported {
		return release.status, errors.New("automatic updates require the packaged macOS app and a matching release asset")
	}
	if err := installDesktopRelease(ctx, desktopUpdateHTTPClient, release); err != nil {
		return release.status, err
	}

	release.status.Restarting = true
	go func() {
		time.Sleep(600 * time.Millisecond)
		wailsruntime.Quit(ctx)
	}()
	return release.status, nil
}

func packagedDesktopApp() bool {
	executable, err := os.Executable()
	return err == nil && appBundlePath(executable) != ""
}

func checkDesktopRelease(
	ctx context.Context,
	client *http.Client,
	endpoint, currentVersion string,
	packaged bool,
) (desktopRelease, error) {
	result := desktopRelease{status: DesktopUpdateStatus{CurrentVersion: strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AgentMux-Desktop-Updater")
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("check for updates: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("check for updates: release service returned %s", response.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return result, fmt.Errorf("check for updates: decode release: %w", err)
	}
	latest := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if _, ok := parseVersion(latest); !ok {
		return result, fmt.Errorf("check for updates: invalid release version %q", release.TagName)
	}
	result.status.LatestVersion = latest
	result.status.ReleaseURL = release.HTMLURL
	result.status.PublishedAt = release.PublishedAt
	result.status.UpdateAvailable = versionLess(result.status.CurrentVersion, latest)

	assetName := desktopAssetName(runtime.GOOS, runtime.GOARCH)
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			result.downloadURL = asset.BrowserDownloadURL
		case assetName + ".sha256":
			result.checksumURL = asset.BrowserDownloadURL
		}
	}
	result.status.Supported = packaged && runtime.GOOS == "darwin" && result.downloadURL != "" && result.checksumURL != ""
	return result, nil
}

func desktopAssetName(goos, goarch string) string {
	return "agentmux-desktop_" + goos + "_" + goarch + ".zip"
}

func parseVersion(value string) ([3]int, bool) {
	var parsed [3]int
	core := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(value), "v"), "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != len(parsed) {
		return parsed, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return parsed, false
		}
		parsed[index] = number
	}
	return parsed, true
}

func versionLess(current, latest string) bool {
	currentParts, currentOK := parseVersion(current)
	latestParts, latestOK := parseVersion(latest)
	if !latestOK {
		return false
	}
	if !currentOK {
		return true
	}
	for index := range currentParts {
		if currentParts[index] != latestParts[index] {
			return currentParts[index] < latestParts[index]
		}
	}
	return false
}

func installDesktopRelease(ctx context.Context, client *http.Client, release desktopRelease) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current application: %w", err)
	}
	targetApp := appBundlePath(executable)
	if targetApp == "" {
		return errors.New("automatic updates require AgentMux.app")
	}
	parent := filepath.Dir(targetApp)
	stageRoot, err := os.MkdirTemp(parent, ".agentmux-update-")
	if err != nil {
		return fmt.Errorf("prepare update beside AgentMux.app: %w", err)
	}
	defer os.RemoveAll(stageRoot)

	archivePath := filepath.Join(stageRoot, "update.zip")
	actualChecksum, err := downloadUpdateFile(ctx, client, release.downloadURL, archivePath, maxDesktopUpdateBytes)
	if err != nil {
		return err
	}
	expectedChecksum, err := downloadUpdateChecksum(ctx, client, release.checksumURL)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualChecksum, expectedChecksum) {
		return fmt.Errorf("verify update: SHA-256 mismatch (got %s, want %s)", actualChecksum, expectedChecksum)
	}

	extractRoot := filepath.Join(stageRoot, "expanded")
	if err := os.Mkdir(extractRoot, 0o700); err != nil {
		return fmt.Errorf("prepare update extraction: %w", err)
	}
	if output, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", archivePath, extractRoot).CombinedOutput(); err != nil {
		return fmt.Errorf("extract update: %w: %s", err, strings.TrimSpace(string(output)))
	}
	stagedApp := filepath.Join(extractRoot, "AgentMux.app")
	stagedExecutable := filepath.Join(stagedApp, "Contents", "MacOS", "AgentMux")
	if info, err := os.Stat(stagedExecutable); err != nil || !info.Mode().IsRegular() {
		return errors.New("verify update: archive does not contain a valid AgentMux.app")
	}
	if output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", stagedApp).CombinedOutput(); err != nil {
		return fmt.Errorf("verify update signature: %w: %s", err, strings.TrimSpace(string(output)))
	}

	backupApp := filepath.Join(parent, fmt.Sprintf(".%s.previous-%d", filepath.Base(targetApp), time.Now().Unix()))
	if err := os.Rename(targetApp, backupApp); err != nil {
		return fmt.Errorf("replace AgentMux.app: move current version aside: %w", err)
	}
	if err := os.Rename(stagedApp, targetApp); err != nil {
		_ = os.Rename(backupApp, targetApp)
		return fmt.Errorf("replace AgentMux.app: activate new version: %w", err)
	}
	if err := startDesktopUpdateRelaunch(targetApp, backupApp); err != nil {
		_ = os.Rename(targetApp, stagedApp)
		_ = os.Rename(backupApp, targetApp)
		return err
	}
	return nil
}

func downloadUpdateFile(ctx context.Context, client *http.Client, rawURL, destination string, maxBytes int64) (string, error) {
	response, err := updateDownload(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.ContentLength > maxBytes {
		return "", errors.New("download update: release asset is too large")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("download update: create archive: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("download update: %w", err)
	}
	if written > maxBytes {
		return "", errors.New("download update: release asset is too large")
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("download update: save archive: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func downloadUpdateChecksum(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	response, err := updateDownload(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 1<<20))
	if !scanner.Scan() {
		return "", errors.New("verify update: checksum file is empty")
	}
	checksum := strings.Fields(scanner.Text())
	if len(checksum) == 0 || len(checksum[0]) != sha256.Size*2 {
		return "", errors.New("verify update: checksum file is invalid")
	}
	if _, err := hex.DecodeString(checksum[0]); err != nil {
		return "", errors.New("verify update: checksum file is invalid")
	}
	return strings.ToLower(checksum[0]), nil
}

func updateDownload(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	if !strings.HasPrefix(rawURL, "https://") {
		return nil, errors.New("download update: release asset URL must use HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "AgentMux-Desktop-Updater")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download update: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("download update: release service returned %s", response.Status)
	}
	return response, nil
}

func startDesktopUpdateRelaunch(targetApp, backupApp string) error {
	script := `while /bin/kill -0 "$1" 2>/dev/null; do /bin/sleep 0.2; done
/usr/bin/open -n "$2"
status=$?
if [ "$status" -eq 0 ]; then
  /bin/sleep 8
  /bin/rm -rf -- "$3"
fi
exit "$status"`
	command := exec.Command("/bin/sh", "-c", script, "agentmux-updater", strconv.Itoa(os.Getpid()), targetApp, backupApp)
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return fmt.Errorf("restart updated AgentMux: %w", err)
	}
	return nil
}
