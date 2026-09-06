package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const githubCLIReleaseBaseURL = "https://github.com/cli/cli/releases/download/"

func githubCLIHomebrewPath(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.Contains(filepath.ToSlash(path), "/Cellar/gh/")
}

// Preserve the existing installation method. A standalone /usr/local/bin/gh
// must not be updated using a package manager that owns a different executable.
func githubCLIPackageCommand(ctx context.Context, executable, action string) ([]string, error) {
	if executable == "" {
		return nil, nil
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	var command []string
	if owner, err := commandOutput(ctx, "dpkg-query", "-S", executable); err == nil &&
		(strings.HasPrefix(owner, "gh: ") || strings.HasPrefix(owner, "gh:")) {
		command = []string{"apt-get", "install", "--only-upgrade", "-y", "gh"}
		if action == "uninstall" {
			command = []string{"apt-get", "remove", "-y", "gh"}
		}
	} else if owner, err := commandOutput(ctx, "rpm", "-qf", "--qf", "%{NAME}", executable); err == nil && strings.TrimSpace(owner) == "gh" {
		manager := ""
		for _, candidate := range []string{"dnf", "yum", "zypper"} {
			if _, err := resolveCLIExecutable(candidate); err == nil {
				manager = candidate
				break
			}
		}
		if manager == "" {
			return nil, fmt.Errorf("GitHub CLI is RPM-managed, but dnf, yum, and zypper are unavailable")
		}
		verb := "upgrade"
		if action == "uninstall" {
			verb = "remove"
		}
		command = []string{manager, verb, "-y", "gh"}
		if manager == "zypper" {
			if verb == "upgrade" {
				verb = "update"
			}
			command = []string{manager, "--non-interactive", verb, "gh"}
		}
	}
	if len(command) > 0 && os.Geteuid() != 0 {
		command = append([]string{"sudo", "-n"}, command...)
	}
	return command, nil
}

func installGitHubCLIRelease(ctx context.Context, spec CLISpec, res CLIInstallResult, installedPath, version, arch string, progress ProgressFunc) CLIInstallResult {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if version == "" {
		latest, err := latestCLIVersion(runCtx, spec)
		if err != nil {
			res.Error = fmt.Sprintf("check GitHub CLI release: %v", err)
			return res
		}
		version = normalizeVersion(latest)
	}
	if version == "" || normalizeVersion(version) != version {
		res.Error = "GitHub CLI release version is invalid"
		return res
	}
	if arch == "arm" {
		arch = "armv6"
	}
	switch arch {
	case "amd64", "arm64", "386", "armv6":
	default:
		res.Error = fmt.Sprintf("GitHub CLI Linux release is unavailable for architecture %s", arch)
		return res
	}
	destination := installedPath
	if destination == "" {
		destination = "/usr/local/bin/gh"
		if os.Geteuid() != 0 {
			home, err := os.UserHomeDir()
			if err != nil {
				res.Error = err.Error()
				return res
			}
			destination = filepath.Join(home, ".local", "bin", "gh")
		}
	} else if resolved, err := filepath.EvalSymlinks(destination); err == nil {
		destination = resolved
	}
	archiveName := fmt.Sprintf("gh_%s_linux_%s.tar.gz", version, arch)
	baseURL := githubCLIReleaseBaseURL + "v" + version + "/"
	res.Command = fmt.Sprintf("install GitHub CLI v%s official Linux %s release to %s", version, arch, destination)
	reportProgress(progress, "downloading", archiveName, 30)
	checksums, err := downloadGitHubCLIAsset(runCtx, baseURL+"gh_"+version+"_checksums.txt", 1<<20)
	if err != nil {
		res.Error = fmt.Sprintf("download GitHub CLI checksums: %v", err)
		return res
	}
	archive, err := downloadReleaseAsset(runCtx, baseURL+archiveName, 128<<20, func(done, total int64) {
		if total > 0 {
			reportProgress(progress, "downloading", fmt.Sprintf("%s: %d / %d bytes", archiveName, done, total), 30+int(done*30/total))
		}
	})
	if err != nil {
		res.Error = fmt.Sprintf("download GitHub CLI release: %v", err)
		return res
	}
	reportProgress(progress, "verifying", "SHA256", 65)
	validate := func(candidate string) error {
		output, err := commandOutput(runCtx, candidate, "--version")
		res.Version = strings.TrimSpace(output)
		if err != nil || normalizeVersion(output) != version {
			return fmt.Errorf("GitHub CLI release verification failed: expected %s, got %q (%v)", version, output, err)
		}
		return nil
	}
	if err := replaceGitHubCLIRelease(archive, string(checksums), archiveName, destination, validate); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Log = fmt.Sprintf("Installed GitHub CLI %s from the official release; SHA256 verified.", version)
	return syncAfterCLIChange(runCtx, spec, res, progress)
}

// Extract only the expected executable and replace it atomically. A failed
// download, checksum, or archive never truncates the working installation.
func replaceGitHubCLIRelease(archive []byte, checksums, archiveName, destination string, validate func(string) error) error {
	want := ""
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == archiveName {
			want = fields[0]
			break
		}
	}
	digest := sha256.Sum256(archive)
	if want == "" || !strings.EqualFold(want, hex.EncodeToString(digest[:])) {
		return fmt.Errorf("GitHub CLI release SHA256 verification failed")
	}
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer zipped.Close()
	reader := tar.NewReader(zipped)
	entry := strings.TrimSuffix(archiveName, ".tar.gz") + "/bin/gh"
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return fmt.Errorf("GitHub CLI release is missing %s", entry)
		}
		if err != nil {
			return err
		}
		if header.Name != entry {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 256<<20 {
			return fmt.Errorf("GitHub CLI release executable is invalid")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		temp, err := os.CreateTemp(filepath.Dir(destination), ".gh-update-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		_, copyErr := io.Copy(temp, reader)
		chmodErr := temp.Chmod(0o755)
		closeErr := temp.Close()
		for _, err := range []error{copyErr, chmodErr, closeErr} {
			if err != nil {
				return err
			}
		}
		if err := validate(temp.Name()); err != nil {
			return err
		}
		return os.Rename(temp.Name(), destination)
	}
}
