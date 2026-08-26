// Package agent aggregates all agent adapters. Importing this package (blank
// import) registers every supported agent with core's registry via each
// adapter's init(). Adapters live in subpackages and are imported below.
package agent

import (
	_ "github.com/wangning19940904/AgentMux/agent/claudecode"
	_ "github.com/wangning19940904/AgentMux/agent/cliagents"
	_ "github.com/wangning19940904/AgentMux/agent/terminal"
)
