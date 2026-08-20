// Package agent aggregates all agent adapters. Importing this package (blank
// import) registers every supported agent with core's registry via each
// adapter's init(). Adapters live in subpackages and are imported below.
package agent

import (
	_ "github.com/wangning19940904/AgentMux/agent/claudecode"
	_ "github.com/wangning19940904/AgentMux/agent/cliagents"
	"github.com/wangning19940904/AgentMux/agent/sdkagent"
	_ "github.com/wangning19940904/AgentMux/agent/terminal"
)

func init() {
	// Register SDK frameworks that are already installed in the sidecar so they
	// appear as routable runtimes. Newly-installed frameworks are picked up on
	// the next daemon start.
	sdkagent.Register()
}
