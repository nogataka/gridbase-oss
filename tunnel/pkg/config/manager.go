package config

import (
	"context"
	"errors"
	"os"
	"sync"
)

// UpdateCallback is invoked when configuration reloads succeed.
type UpdateCallback func(*Config)

// RuntimeState exposes runtime health for configuration sources.
type RuntimeState struct {
	Degraded bool
	Reason   string
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// Manager provides thread-safe access to runtime configuration with hot-reload support.
type Manager struct {
	mu       sync.RWMutex
	current  *Config
	fallback *Config
	// fileDomains are the domains declared in the configuration file (static).
	fileDomains []DomainConfig
	// externalDomains are domains supplied by an external source such as Directus.
	// They are re-merged on top of fileDomains whenever the file is reloaded so a
	// file reload never drops them.
	externalDomains []DomainConfig
	sourcePath      string
	env             EnvProvider
	watchers        map[int]UpdateCallback
	nextWatcherID   int
	state           RuntimeState
}

// NewManager initializes a Manager with an already validated configuration.
func NewManager(cfg *Config, opts ...ManagerOption) *Manager {
	if cfg == nil {
		panic("config.NewManager requires a valid configuration")
	}

	cloned := cfg.Clone()
	m := &Manager{
		current:  cloned,
		fallback: cloned.Clone(),
		env:      os.LookupEnv,
		watchers: make(map[int]UpdateCallback),
	}
	m.fileDomains = cloneDomains(cloned.Domains)
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// WithFileSource sets the configuration file used for reload operations.
func WithFileSource(path string) ManagerOption {
	return func(m *Manager) {
		m.sourcePath = path
	}
}

// WithEnvProvider configures the environment provider used during reloads.
func WithEnvProvider(env EnvProvider) ManagerOption {
	return func(m *Manager) {
		if env != nil {
			m.env = env
		}
	}
}

// Current returns a deep copy of the active configuration.
func (m *Manager) Current() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current.Clone()
}

// Fallback returns the last known-good configuration.
func (m *Manager) Fallback() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fallback.Clone()
}

// State returns the runtime state flags.
func (m *Manager) State() RuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// MarkDegraded toggles degraded mode with an optional reason.
func (m *Manager) MarkDegraded(reason error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Degraded = true
	if reason != nil {
		m.state.Reason = reason.Error()
	} else {
		m.state.Reason = ""
	}
}

// ClearDegraded resets degraded mode information.
func (m *Manager) ClearDegraded() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = RuntimeState{}
}

// Subscribe registers a callback for configuration updates and returns an unsubscribe function.
func (m *Manager) Subscribe(cb UpdateCallback) func() {
	if cb == nil {
		return func() {}
	}
	m.mu.Lock()
	id := m.nextWatcherID
	m.nextWatcherID++
	m.watchers[id] = cb
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		delete(m.watchers, id)
		m.mu.Unlock()
	}
}

// Watch registers a callback and blocks until the context is cancelled.
// The callback is first registered via Subscribe to guarantee no updates are
// missed, then the current configuration snapshot is delivered as the initial
// call. This ordering ensures that any Replace/Reload that races between the
// initial snapshot delivery and the subscription is still observed by the caller.
func (m *Manager) Watch(ctx context.Context, cb UpdateCallback) error {
	if cb == nil {
		return errors.New("config.Manager: update callback required")
	}

	unsubscribe := m.Subscribe(func(cfg *Config) {
		cb(cfg.Clone())
	})
	defer unsubscribe()

	cb(m.Current())

	<-ctx.Done()
	return ctx.Err()
}

// Reload refreshes configuration from the configured source path.
func (m *Manager) Reload(ctx context.Context) (*Config, error) {
	m.mu.RLock()
	path := m.sourcePath
	env := m.env
	m.mu.RUnlock()

	if path == "" {
		return nil, errors.New("config.Manager: no source path configured")
	}

	cfg, err := LoadWithEnv(path, env)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.fileDomains = cloneDomains(cfg.Domains)
	cfg.Domains = MergeDomains(m.fileDomains, m.externalDomains)
	m.mu.Unlock()

	m.Replace(cfg)
	return m.Current(), nil
}

// ApplyExternalDomains installs the domains supplied by an external source
// (Directus) and merges them with the domains declared in the configuration
// file. The external set is remembered so that Reload re-applies it.
// On a subdomain conflict the external entry wins.
func (m *Manager) ApplyExternalDomains(external []DomainConfig) (*Config, error) {
	m.mu.Lock()
	m.externalDomains = cloneDomains(external)
	cfg := m.current.Clone()
	cfg.Domains = MergeDomains(m.fileDomains, m.externalDomains)
	m.mu.Unlock()

	return m.ApplySnapshot(cfg)
}

// MergeDomains combines static and external domains. Entries are keyed by
// subdomain; an external entry replaces a static entry with the same subdomain.
// The result is ordered static-first (file order), then external (given order).
func MergeDomains(static, external []DomainConfig) []DomainConfig {
	if len(external) == 0 {
		return cloneDomains(static)
	}
	override := make(map[string]struct{}, len(external))
	for _, d := range external {
		override[d.Subdomain] = struct{}{}
	}
	merged := make([]DomainConfig, 0, len(static)+len(external))
	for _, d := range static {
		if _, replaced := override[d.Subdomain]; replaced {
			continue
		}
		merged = append(merged, cloneDomainConfig(d))
	}
	for _, d := range external {
		merged = append(merged, cloneDomainConfig(d))
	}
	return merged
}

func cloneDomains(src []DomainConfig) []DomainConfig {
	if src == nil {
		return nil
	}
	out := make([]DomainConfig, len(src))
	for i, d := range src {
		out[i] = cloneDomainConfig(d)
	}
	return out
}

// ApplySnapshot validates and applies an externally supplied configuration snapshot.
func (m *Manager) ApplySnapshot(snapshot *Config) (*Config, error) {
	if snapshot == nil {
		return nil, errors.New("config.Manager: snapshot is nil")
	}

	clone := snapshot.Clone()
	if err := applyEnvOverrides(clone, m.env); err != nil {
		return nil, err
	}
	if err := clone.Validate(); err != nil {
		return nil, err
	}

	m.Replace(clone)
	return m.Current(), nil
}

// Replace swaps the active configuration and notifies watchers.
func (m *Manager) Replace(cfg *Config) {
	if cfg == nil {
		return
	}

	snapshot := cfg.Clone()

	m.mu.Lock()
	m.current = snapshot
	m.fallback = snapshot.Clone()
	watchers := make([]UpdateCallback, 0, len(m.watchers))
	for _, cb := range m.watchers {
		watchers = append(watchers, cb)
	}
	m.mu.Unlock()

	m.fireCallbacks(watchers, snapshot)
}

func (m *Manager) fireCallbacks(callbacks []UpdateCallback, cfg *Config) {
	for _, cb := range callbacks {
		if cb == nil {
			continue
		}
		callback := cb
		snapshot := cfg.Clone()
		go callback(snapshot)
	}
}
