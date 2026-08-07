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

// MemoryFactory builds a MemoryStore from its config map.
type MemoryFactory func(cfg map[string]any) (MemoryStore, error)

// SkillFactory builds a SkillManager from its config map.
type SkillFactory func(cfg map[string]any) (SkillManager, error)

// MCPFactory builds an MCPRegistry from its config map.
type MCPFactory func(cfg map[string]any) (MCPRegistry, error)

// GuardFactory builds a Guard from its config map.
type GuardFactory func(cfg map[string]any) (Guard, error)

var (
	regMu     sync.RWMutex
	platforms = map[string]PlatformFactory{}
	agents    = map[string]AgentFactory{}
	memories  = map[string]MemoryFactory{}
	skillMgrs = map[string]SkillFactory{}
	mcpRegs   = map[string]MCPFactory{}
	guards    = map[string]GuardFactory{}
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

// RegisterMemory registers a memory-store factory under name.
func RegisterMemory(name string, f MemoryFactory) { register(memories, "memory", name, f) }

// RegisterSkillManager registers a skill-manager factory under name.
func RegisterSkillManager(name string, f SkillFactory) {
	register(skillMgrs, "skill manager", name, f)
}

// RegisterMCPRegistry registers an MCP-registry factory under name.
func RegisterMCPRegistry(name string, f MCPFactory) { register(mcpRegs, "mcp registry", name, f) }

// RegisterGuard registers a guard factory under name.
func RegisterGuard(name string, f GuardFactory) { register(guards, "guard", name, f) }

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

// RegisteredMemories returns the sorted names of registered memory stores.
func RegisteredMemories() []string { return sortedKeys(memories) }

// RegisteredSkillManagers returns the sorted names of registered skill managers.
func RegisteredSkillManagers() []string { return sortedKeys(skillMgrs) }

// RegisteredMCPRegistries returns the sorted names of registered MCP registries.
func RegisteredMCPRegistries() []string { return sortedKeys(mcpRegs) }

// RegisteredGuards returns the sorted names of registered guards.
func RegisteredGuards() []string { return sortedKeys(guards) }
