package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"
)

const daemonRetryInterval = 2 * time.Second

// Reserve the API address before initializing any daemon components. A second
// CLI or desktop process stays available as a standby while its UI uses the
// existing API, and takes over when that backend exits.
func waitForDaemonListener(ctx context.Context, log *slog.Logger, addr string, retryInterval time.Duration) (net.Listener, error) {
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err == nil {
			if waiting {
				log.Info("management address available; starting backend", "addr", addr)
			}
			return listener, nil
		}
		if !isAddressInUse(err) {
			return nil, fmt.Errorf("listen for management API %s: %w", addr, err)
		}
		if !waiting {
			log.Info("management address in use; waiting as standby", "addr", addr, "retry_in", retryInterval)
			waiting = true
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
