package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"
)

type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type DirectoryListing struct {
	Path       string           `json:"path"`
	ParentPath string           `json:"parent_path,omitempty"`
	Entries    []DirectoryEntry `json:"entries"`
}

// ListDirectories reads a host's filesystem through SSH. It intentionally
// does not depend on the AgentMux HTTP version running on that host.
func (m *Manager) ListDirectories(ctx context.Context, id, rawPath string) (DirectoryListing, error) {
	command := remoteDirectoryPathScript(rawPath, true) + `
if [ ! -d "$target" ]; then
  printf '%s\n' 'path is not a directory' >&2
  exit 2
fi
target=$(cd "$target" && pwd -P)
parent=$(dirname "$target")
if [ "$parent" = "$target" ]; then parent=''; fi
printf '%s\0%s\0' "$target" "$parent"
for entry in "$target"/* "$target"/.[!.]* "$target"/..?*; do
  if [ -d "$entry" ]; then
    name=${entry##*/}
    printf '%s\0%s\0' "$name" "$entry"
  fi
done`
	output, err := m.runCommand(ctx, id, command)
	if err != nil {
		return DirectoryListing{}, err
	}
	return parseDirectoryListing(output)
}

// EnsureDirectory creates a directory through SSH and returns its canonical
// absolute path on the selected host.
func (m *Manager) EnsureDirectory(ctx context.Context, id, rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", errors.New("directory path is required")
	}
	command := remoteDirectoryPathScript(rawPath, false) + `
mkdir -p "$target"
target=$(cd "$target" && pwd -P)
printf '%s\0' "$target"`
	output, err := m.runCommand(ctx, id, command)
	if err != nil {
		return "", err
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) < 2 || len(parts[0]) == 0 {
		return "", errors.New("remote directory command returned an invalid path")
	}
	return pathpkg.Clean(string(parts[0])), nil
}

func remoteDirectoryPathScript(rawPath string, emptyMeansHome bool) string {
	emptyTarget := `
  target=$HOME`
	if !emptyMeansHome {
		emptyTarget = `
  printf '%s\n' 'directory path is required' >&2
  exit 2`
	}
	return `set -eu
raw=` + shellQuote(strings.TrimSpace(rawPath)) + `
case "$raw" in
'')` + emptyTarget + ` ;;
'~') target=$HOME ;;
'~/'*) target=$HOME/${raw#\~/} ;;
'~'*)
  printf '%s\n' 'only current-user home paths are supported' >&2
  exit 2
  ;;
/*) target=$raw ;;
*) target=$HOME/$raw ;;
esac`
}

func parseDirectoryListing(output []byte) (DirectoryListing, error) {
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || (len(parts)-2)%2 != 0 || len(parts[0]) == 0 {
		return DirectoryListing{}, errors.New("remote directory command returned invalid data")
	}
	listing := DirectoryListing{
		Path:       pathpkg.Clean(string(parts[0])),
		ParentPath: string(parts[1]),
		Entries:    make([]DirectoryEntry, 0, (len(parts)-2)/2),
	}
	if listing.ParentPath != "" {
		listing.ParentPath = pathpkg.Clean(listing.ParentPath)
	}
	for index := 2; index < len(parts); index += 2 {
		listing.Entries = append(listing.Entries, DirectoryEntry{
			Name: string(parts[index]), Path: pathpkg.Clean(string(parts[index+1])),
		})
	}
	sort.Slice(listing.Entries, func(i, j int) bool {
		left := strings.ToLower(listing.Entries[i].Name)
		right := strings.ToLower(listing.Entries[j].Name)
		if left == right {
			return listing.Entries[i].Name < listing.Entries[j].Name
		}
		return left < right
	})
	return listing, nil
}

func (m *Manager) runCommand(ctx context.Context, id, command string) ([]byte, error) {
	host, ok := m.store.Get(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := m.client(ctx, host)
		if err != nil {
			return nil, err
		}
		output, err := client.Run(ctx, command, nil)
		if err == nil {
			return output, nil
		}
		message := strings.TrimSpace(string(output))
		if message != "" {
			return nil, fmt.Errorf("run SSH command: %w: %s", err, message)
		}
		m.invalidate(id)
		if attempt == 1 {
			return nil, fmt.Errorf("run SSH command: %w", err)
		}
	}
	return nil, errors.New("run SSH command failed")
}
