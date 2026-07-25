package usage

import (
	"context"
	"time"

	"github.com/wangning19940904/AgentMux/usage/parser"
	"github.com/wangning19940904/AgentMux/usage/ssh"
)

// CollectSSH connects to each configured SSH target, syncs remote session logs
// into a local staging directory, then runs the same parsers over them. This
// satisfies requirement 2.4: token statistics for SSH-based usage.
func (e *Engine) CollectSSH(ctx context.Context, since time.Time) error {
	for _, tgt := range e.cfg.Usage.SSHTargets {
		e.log.Info("ssh collect", "target", tgt.Name, "host", tgt.Host)
		staging, err := ssh.Sync(ctx, ssh.Target{
			Name: tgt.Name, Host: tgt.Host, Port: tgt.Port, User: tgt.User,
			KeyPath: tgt.KeyPath, Password: tgt.Password,
			Sources: tgt.Sources, Paths: tgt.Paths,
		}, e.log)
		if err != nil {
			e.log.Warn("ssh sync failed", "target", tgt.Name, "err", err)
			continue
		}
		sources := tgt.Sources
		if len(sources) == 0 {
			sources = e.cfg.Usage.Sources
		}
		for _, src := range sources {
			root := staging.Root(src)
			if root == "" {
				continue
			}
			col, err := parser.NewCollector(src, root, nil)
			if err != nil {
				continue
			}
			recs, err := col.Collect(ctx, since)
			if err != nil {
				e.log.Warn("ssh parse failed", "source", src, "err", err)
				continue
			}
			for i := range recs {
				recs[i].Host = tgt.Name
			}
			e.price(recs)
			if err := e.st.UpsertUsage(ctx, recs); err != nil {
				return err
			}
			e.log.Debug("ssh collected", "target", tgt.Name, "source", src, "records", len(recs))
		}
	}
	return nil
}
