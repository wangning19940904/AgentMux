package ssh

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// copyRemoteTree streams a remote directory (relative to the remote home) to a
// local root by running `tar` on the remote and extracting the stream locally.
// This avoids an external SFTP dependency and handles nested session trees.
// It returns the number of regular files extracted.
func copyRemoteTree(ctx context.Context, client *ssh.Client, remoteRel, localRoot string) (int, error) {
	sess, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer sess.Close()

	var stderr bytes.Buffer
	sess.Stderr = &stderr
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return 0, err
	}

	// Only pull session/log files we care about to keep transfers small.
	// -C "$HOME" makes paths relative to the remote home dir.
	cmd := fmt.Sprintf(
		`cd "$HOME" && [ -e %q ] && tar cf - --exclude='*.tmp' %q 2>/dev/null || true`,
		remoteRel, remoteRel)
	if err := sess.Start(cmd); err != nil {
		return 0, err
	}

	count, extractErr := extractTar(ctx, stdout, localRoot, remoteRel)
	waitErr := sess.Wait()
	if extractErr != nil {
		return count, extractErr
	}
	if waitErr != nil {
		// tar may exit non-zero on benign warnings; only fail if nothing came.
		if count == 0 {
			return 0, fmt.Errorf("remote tar: %v (%s)", waitErr, strings.TrimSpace(stderr.String()))
		}
	}
	return count, nil
}

// extractTar extracts a tar stream into localRoot, stripping the remoteRel
// prefix so files land directly under localRoot.
func extractTar(ctx context.Context, r io.Reader, localRoot, remoteRel string) (int, error) {
	tr := tar.NewReader(r)
	count := 0
	prefix := strings.TrimSuffix(remoteRel, "/") + "/"
	for {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		name := strings.TrimPrefix(hdr.Name, prefix)
		if name == hdr.Name {
			name = strings.TrimPrefix(name, remoteRel)
		}
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		target := filepath.Join(localRoot, filepath.Clean(name))
		if !strings.HasPrefix(target, filepath.Clean(localRoot)+string(os.PathSeparator)) &&
			target != filepath.Clean(localRoot) {
			continue // guard against path traversal
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			f, err := os.Create(target)
			if err != nil {
				return count, err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return count, err
			}
			f.Close()
			count++
		}
	}
	return count, nil
}
