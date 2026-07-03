// Package agent aggregates all agent adapters. Importing this package (blank
// import) registers every supported agent with core's registry via each
// adapter's init(). Adapters live in subpackages and are imported below.
package agent

import (
	_ "github.com/agentnexus/agentnexus/agent/claudecode"
	_ "github.com/agentnexus/agentnexus/agent/cliagents"
)
