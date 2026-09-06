package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const releaseChunkSize int64 = 256 << 10

var releaseHTTPClient = newReleaseHTTPClient()

func newReleaseHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// HTTP/2 would multiplex every range onto one congested TCP connection.
	// Independent HTTP/1.1 connections make the bounded workers effective on
	// slow release-CDN routes while retaining normal TLS verification.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.MaxConnsPerHost = 6
	transport.MaxIdleConnsPerHost = 6
	return &http.Client{Transport: transport}
}

// Download large release assets in bounded parallel ranges. Slow or reset CDN
// connections retry only their chunk, rather than restarting the entire file.
// Servers without range support may return one bounded 200 response instead.
func downloadGitHubCLIAsset(ctx context.Context, assetURL string, limit int64) ([]byte, error) {
	return downloadReleaseAsset(ctx, assetURL, limit, nil)
}

func downloadReleaseAsset(ctx context.Context, assetURL string, limit int64, progress func(int64, int64)) ([]byte, error) {
	end := int64(-1)
	if limit > 1<<20 {
		end = releaseChunkSize - 1
	}
	first, total, finalURL, err := downloadReleaseChunk(ctx, assetURL, 0, end, limit, 0)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(int64(len(first)), total)
	}
	if int64(len(first)) == total {
		return first, nil
	}
	data := make([]byte, int(total))
	copy(data, first)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	next, completed := int64(len(first)), int64(len(first))
	var firstErr error
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				start := next
				next += releaseChunkSize
				stop := firstErr != nil || start >= total
				mu.Unlock()
				if stop {
					return
				}
				end := min(start+releaseChunkSize-1, total-1)
				chunk, _, _, err := downloadReleaseChunk(ctx, finalURL, start, end, limit, total)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
				copy(data[start:end+1], chunk)
				completed += int64(len(chunk))
				if progress != nil {
					progress(completed, total)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return data, nil
}

func downloadReleaseChunk(ctx context.Context, assetURL string, start, end, limit, expectedTotal int64) ([]byte, int64, string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, "", err
		}
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, 0, "", ctx.Err()
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		data, total, finalURL, retry, err := readReleaseChunk(attemptCtx, assetURL, start, end, limit, expectedTotal)
		cancel()
		if err == nil {
			return data, total, finalURL, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return nil, 0, "", lastErr
}

func readReleaseChunk(ctx context.Context, assetURL string, start, end, limit, expectedTotal int64) ([]byte, int64, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, 0, "", false, err
	}
	if end >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}
	resp, err := releaseHTTPClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, 0, "", true, err
	}
	defer resp.Body.Close()
	finalURL := assetURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, 0, "", resp.StatusCode == 429 || resp.StatusCode >= 500, fmt.Errorf("release download HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		if start != 0 || expectedTotal != 0 {
			return nil, 0, "", false, fmt.Errorf("release server ignored a required byte range")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, 0, "", true, err
		}
		if int64(len(data)) > limit {
			return nil, 0, "", false, fmt.Errorf("release exceeds %d bytes", limit)
		}
		return data, int64(len(data)), finalURL, false, nil
	}
	var from, through, total int64
	contentRange := resp.Header.Get("Content-Range")
	_, err = fmt.Sscanf(contentRange, "bytes %d-%d/%d", &from, &through, &total)
	if err != nil || total < 1 || total > limit || from != start || end < 0 || through != min(end, total-1) ||
		contentRange != fmt.Sprintf("bytes %d-%d/%d", from, through, total) || expectedTotal != 0 && total != expectedTotal {
		return nil, 0, "", false, fmt.Errorf("release server returned an invalid Content-Range")
	}
	length := through - from + 1
	if length < 1 {
		return nil, 0, "", false, fmt.Errorf("release server returned an empty range")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil {
		return nil, 0, "", true, err
	}
	if int64(len(data)) != length {
		return nil, 0, "", true, io.ErrUnexpectedEOF
	}
	return data, total, finalURL, false, nil
}
