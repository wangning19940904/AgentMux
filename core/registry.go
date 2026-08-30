package core

import (
	"fmt"
	"sort"
	"sync"
)

// PlatformFactory builds a Platform from its raw TOML config map.
type PlatformFactory func(cfg map[string]any) (Platform, error)

// AgentFactory builds an Agent from its raw TOML config map.
type AgentFactory func(cfg map[string]any) (Agent, error)

var (
	regMu     sync.RWMutex
	platforms = map[string]PlatformFactory{}
	agents    = map[string]AgentFactory{}
)

// register adds a factory to a registry map, panicking on duplicate names.
func register[F any](m map[string]F, kind, name string, f F) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := m[name]; dup {
		panic(fmt.Sprintf("core: %s %q already registered", kind, name))
	}
	m[name] = f
}

// sortedKeys returns the sorted names present in a registry map.
func sortedKeys[F any](m map[string]F) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RegisterPlatform registers a platform factory under name. Called from
// adapter init() functions.
func RegisterPlatform(name string, f PlatformFactory) { register(platforms, "platform", name, f) }

// RegisterAgent registers an agent factory under name.
func RegisterAgent(name string, f AgentFactory) { register(agents, "agent", name, f) }

// CreatePlatform instantiates a registered platform.
func CreatePlatform(name string, cfg map[string]any) (Platform, error) {
	regMu.RLock()
	f, ok := platforms[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown platform %q", name)
	}
	return f(cfg)
}

// CreateAgent instantiates a registered agent.
func CreateAgent(name string, cfg map[string]any) (Agent, error) {
	regMu.RLock()
	f, ok := agents[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown agent %q", name)
	}
	return f(cfg)
}

// HasAgent reports whether an agent factory is registered under name.
func HasAgent(name string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	_, ok := agents[name]
	return ok
}

// RegisteredPlatforms returns the sorted names of registered platforms.
func RegisteredPlatforms() []string { return sortedKeys(platforms) }

// RegisteredAgents returns the sorted names of registered agents.
func RegisteredAgents() []string { return sortedKeys(agents) }
