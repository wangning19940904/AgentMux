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

// CollectorFactory builds a UsageCollector from its config map.
type CollectorFactory func(cfg map[string]any) (UsageCollector, error)

// MemoryFactory builds a MemoryStore from its config map.
type MemoryFactory func(cfg map[string]any) (MemoryStore, error)

// SkillFactory builds a SkillManager from its config map.
type SkillFactory func(cfg map[string]any) (SkillManager, error)

// MCPFactory builds an MCPRegistry from its config map.
type MCPFactory func(cfg map[string]any) (MCPRegistry, error)

// GuardFactory builds a Guard from its config map.
type GuardFactory func(cfg map[string]any) (Guard, error)

var (
	regMu      sync.RWMutex
	platforms  = map[string]PlatformFactory{}
	agents     = map[string]AgentFactory{}
	collectors = map[string]CollectorFactory{}
	memories   = map[string]MemoryFactory{}
	skillMgrs  = map[string]SkillFactory{}
	mcpRegs    = map[string]MCPFactory{}
	guards     = map[string]GuardFactory{}
)

// RegisterPlatform registers a platform factory under name. Called from
// adapter init() functions.
func RegisterPlatform(name string, f PlatformFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := platforms[name]; dup {
		panic(fmt.Sprintf("core: platform %q already registered", name))
	}
	platforms[name] = f
}

// RegisterAgent registers an agent factory under name.
func RegisterAgent(name string, f AgentFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := agents[name]; dup {
		panic(fmt.Sprintf("core: agent %q already registered", name))
	}
	agents[name] = f
}

// RegisterCollector registers a usage collector factory under name.
func RegisterCollector(name string, f CollectorFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := collectors[name]; dup {
		panic(fmt.Sprintf("core: collector %q already registered", name))
	}
	collectors[name] = f
}

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

// CreateCollector instantiates a registered collector.
func CreateCollector(name string, cfg map[string]any) (UsageCollector, error) {
	regMu.RLock()
	f, ok := collectors[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown collector %q", name)
	}
	return f(cfg)
}

// RegisterMemory registers a memory-store factory under name.
func RegisterMemory(name string, f MemoryFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := memories[name]; dup {
		panic(fmt.Sprintf("core: memory %q already registered", name))
	}
	memories[name] = f
}

// RegisterSkillManager registers a skill-manager factory under name.
func RegisterSkillManager(name string, f SkillFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := skillMgrs[name]; dup {
		panic(fmt.Sprintf("core: skill manager %q already registered", name))
	}
	skillMgrs[name] = f
}

// RegisterMCPRegistry registers an MCP-registry factory under name.
func RegisterMCPRegistry(name string, f MCPFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := mcpRegs[name]; dup {
		panic(fmt.Sprintf("core: mcp registry %q already registered", name))
	}
	mcpRegs[name] = f
}

// RegisterGuard registers a guard factory under name.
func RegisterGuard(name string, f GuardFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := guards[name]; dup {
		panic(fmt.Sprintf("core: guard %q already registered", name))
	}
	guards[name] = f
}

// CreateMemory instantiates a registered memory store.
func CreateMemory(name string, cfg map[string]any) (MemoryStore, error) {
	regMu.RLock()
	f, ok := memories[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown memory %q", name)
	}
	return f(cfg)
}

// CreateSkillManager instantiates a registered skill manager.
func CreateSkillManager(name string, cfg map[string]any) (SkillManager, error) {
	regMu.RLock()
	f, ok := skillMgrs[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown skill manager %q", name)
	}
	return f(cfg)
}

// CreateMCPRegistry instantiates a registered MCP registry.
func CreateMCPRegistry(name string, cfg map[string]any) (MCPRegistry, error) {
	regMu.RLock()
	f, ok := mcpRegs[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown mcp registry %q", name)
	}
	return f(cfg)
}

// CreateGuard instantiates a registered guard.
func CreateGuard(name string, cfg map[string]any) (Guard, error) {
	regMu.RLock()
	f, ok := guards[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("core: unknown guard %q", name)
	}
	return f(cfg)
}

// RegisteredPlatforms returns the sorted names of registered platforms.
func RegisteredPlatforms() []string { return sortedKeys(platforms) }

// RegisteredAgents returns the sorted names of registered agents.
func RegisteredAgents() []string { return sortedKeysA(agents) }

// HasAgent reports whether an agent factory is registered under name.
func HasAgent(name string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	_, ok := agents[name]
	return ok
}

// RegisteredCollectors returns the sorted names of registered collectors.
func RegisteredCollectors() []string { return sortedKeysC(collectors) }

// RegisteredMemories returns the sorted names of registered memory stores.
func RegisteredMemories() []string { return sortedKeysM(memories) }

// RegisteredSkillManagers returns the sorted names of registered skill managers.
func RegisteredSkillManagers() []string { return sortedKeysS(skillMgrs) }

// RegisteredMCPRegistries returns the sorted names of registered MCP registries.
func RegisteredMCPRegistries() []string { return sortedKeysMCP(mcpRegs) }

// RegisteredGuards returns the sorted names of registered guards.
func RegisteredGuards() []string { return sortedKeysG(guards) }

func sortedKeys(m map[string]PlatformFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysA(m map[string]AgentFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysC(m map[string]CollectorFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysM(m map[string]MemoryFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysS(m map[string]SkillFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysMCP(m map[string]MCPFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysG(m map[string]GuardFactory) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
