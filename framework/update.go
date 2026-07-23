package framework

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UpdateCheck reports whether an installed framework has a newer version.
type UpdateCheck struct {
	Kind            string    `json:"kind"`
	Display         string    `json:"display,omitempty"`
	Installed       bool      `json:"installed"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// CheckUpdate compares an installed framework with its catalog-owned latest
// version source. It never mutates the installation.
func CheckUpdate(ctx context.Context, kind string) UpdateCheck {
	kind = strings.TrimSpace(kind)
	res := UpdateCheck{Kind: kind, CheckedAt: time.Now()}
	spec, ok := Lookup(kind)
	if !ok {
		res.Error = fmt.Sprintf("unknown framework %q", kind)
		return res
	}
	res.Display = spec.Display
	if !spec.Supported || !spec.UpdateSupported {
		res.Error = fmt.Sprintf("framework %q does not support update checks", kind)
		return res
	}

	pre := DetectPrereqs()
	status := Detect(spec, pre)
	res.Installed = status.Installed
	if !status.Installed {
		res.Error = fmt.Sprintf("framework %q is not installed", kind)
		return res
	}
	current := normalizeSDKVersion(status.Version)
	if current == "" {
		res.Error = fmt.Sprintf("could not parse installed version from %q", status.Version)
		return res
	}
	res.CurrentVersion = current

	latest, err := latestFrameworkVersion(ctx, spec, pre)
	if err != nil {
		res.Error = fmt.Sprintf("check update failed: %v", err)
		return res
	}
	latest = normalizeSDKVersion(latest)
	if latest == "" {
		res.Error = "registry did not return a latest version"
		return res
	}
	res.LatestVersion = latest
	res.UpdateAvailable = frameworkUpdateAvailable(spec, latest, current)
	return res
}

func frameworkUpdateAvailable(spec Spec, latest, current string) bool {
	if spec.LatestURL != "" {
		// Self-updating native CLIs may use date+build identifiers whose hash
		// suffix has no semantic ordering. The official endpoint is the source of
		// truth, so any different build identifier is an available update.
		return latest != current
	}
	return sdkVersionGreater(latest, current)
}

func latestFrameworkVersion(ctx context.Context, spec Spec, pre Prereqs) (string, error) {
	switch spec.KindType {
	case KindSDK:
		if spec.Language != "node" || len(spec.Packages) == 0 {
			return "", fmt.Errorf("SDK does not have a supported registry package")
		}
		if !pre.NPM {
			return "", fmt.Errorf("npm not found on PATH; install Node.js first")
		}
		return npmPackageVersion(ctx, spec.Packages[0])
	case KindCLI:
		if spec.NPMPackage != "" {
			if !pre.NPM {
				return "", fmt.Errorf("npm not found on PATH; install Node.js first")
			}
			return npmPackageVersion(ctx, spec.NPMPackage)
		}
		if spec.LatestURL != "" {
			return officialCLIVersion(ctx, spec.LatestURL)
		}
	}
	return "", fmt.Errorf("no latest-version source configured")
}

func npmPackageVersion(ctx context.Context, pkg string) (string, error) {
	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "npm", "view", pkg, "version", "--silent")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func officialCLIVersion(ctx context.Context, url string) (string, error) {
	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("latest-version endpoint returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	version := cursorInstallerVersion(string(body))
	if version == "" {
		return "", fmt.Errorf("latest-version endpoint returned no recognizable version")
	}
	return version, nil
}

var cursorInstallerVersionRE = regexp.MustCompile(`(?:/lab/|versions/)(\d{4}\.\d{2}\.\d{2}-[0-9A-Za-z]+)(?:/|\b)`)

func cursorInstallerVersion(raw string) string {
	match := cursorInstallerVersionRE.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

var sdkVersionRE = regexp.MustCompile(`\d+(?:\.\d+){0,3}(?:[-+][0-9A-Za-z.-]+)?`)

func normalizeSDKVersion(raw string) string {
	return sdkVersionRE.FindString(strings.TrimSpace(raw))
}

func sdkVersionGreater(candidate, current string) bool {
	candidateParts, ok := sdkNumericVersionParts(candidate)
	if !ok {
		return false
	}
	currentParts, ok := sdkNumericVersionParts(current)
	if !ok {
		return false
	}
	for i := range candidateParts {
		if candidateParts[i] > currentParts[i] {
			return true
		}
		if candidateParts[i] < currentParts[i] {
			return false
		}
	}
	candidatePre := sdkPrerelease(candidate)
	currentPre := sdkPrerelease(current)
	if candidatePre == currentPre {
		return false
	}
	if candidatePre == "" {
		return currentPre != ""
	}
	if currentPre == "" {
		return false
	}
	return compareSDKPrerelease(candidatePre, currentPre) > 0
}

func sdkPrerelease(version string) string {
	withoutBuild := strings.SplitN(strings.TrimSpace(version), "+", 2)[0]
	parts := strings.SplitN(withoutBuild, "-", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func compareSDKPrerelease(candidate, current string) int {
	candidateParts := strings.Split(candidate, ".")
	currentParts := strings.Split(current, ".")
	for i := 0; i < len(candidateParts) && i < len(currentParts); i++ {
		if candidateParts[i] == currentParts[i] {
			continue
		}
		candidateNumber, candidateErr := strconv.Atoi(candidateParts[i])
		currentNumber, currentErr := strconv.Atoi(currentParts[i])
		switch {
		case candidateErr == nil && currentErr == nil:
			if candidateNumber > currentNumber {
				return 1
			}
			return -1
		case candidateErr == nil:
			return -1
		case currentErr == nil:
			return 1
		case candidateParts[i] > currentParts[i]:
			return 1
		default:
			return -1
		}
	}
	if len(candidateParts) > len(currentParts) {
		return 1
	}
	if len(candidateParts) < len(currentParts) {
		return -1
	}
	return 0
}

func sdkNumericVersionParts(version string) ([4]int, bool) {
	var parts [4]int
	base := strings.SplitN(strings.SplitN(strings.TrimSpace(version), "-", 2)[0], "+", 2)[0]
	if base == "" {
		return parts, false
	}
	for i, raw := range strings.Split(base, ".") {
		if i >= len(parts) || raw == "" {
			return parts, false
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return parts, false
		}
		parts[i] = value
	}
	return parts, true
}
