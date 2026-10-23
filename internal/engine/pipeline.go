package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Stage identifies one step of the search/grab pipeline.
type Stage string

const (
	StagePlan       Stage = "plan"
	StageRequest    Stage = "request"
	StageFetch      Stage = "fetch"
	StageExtract    Stage = "extract"
	StageNormalize  Stage = "normalize"
	StageEnrich     Stage = "enrich"
	StageDedup      Stage = "dedup"
	StageStore      Stage = "store"
	StageDistribute Stage = "distribute"
)

// Job is the state carried through one pipeline invocation. A stage fills the
// fields it owns; later stages read them.
type Job struct {
	Indexer   IndexerRef
	Connector Connector
	Search    SearchRequest
	Latest    *LatestRequest
	Response  *Response
	Items     []Item
	Payload   *Payload
}

// Handler is the terminal function of a stage.
type Handler func(context.Context, *Job) error

// Middleware wraps a handler. The first middleware registered with Use is the
// outermost, so it observes the stage both before and after the rest.
type Middleware func(Handler) Handler

// Registry holds the per-stage middleware chains, the connector factories and
// the registered modules. It is safe for concurrent use.
type Registry struct {
	mu         sync.RWMutex
	middleware map[Stage][]Middleware
	factories  map[string]ConnectorFactory
	modules    map[string]struct{}
}

// NewRegistry returns an empty registry with initialized maps.
func NewRegistry() *Registry {
	return &Registry{
		middleware: make(map[Stage][]Middleware),
		factories:  make(map[string]ConnectorFactory),
		modules:    make(map[string]struct{}),
	}
}

// Use appends mw to the middleware chain of stage. Nil middleware is ignored.
func (r *Registry) Use(stage Stage, mw Middleware) {
	if mw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.middleware == nil {
		r.middleware = make(map[Stage][]Middleware)
	}
	r.middleware[stage] = append(r.middleware[stage], mw)
}

// Invoke runs terminal for stage, wrapped by that stage's middleware in
// registration order. Middleware errors are returned unchanged unless the
// middleware itself wraps them.
func (r *Registry) Invoke(ctx context.Context, stage Stage, job *Job, terminal Handler) error {
	if job == nil {
		return errors.New("engine: nil job")
	}
	if terminal == nil {
		return errors.New("engine: nil terminal handler")
	}

	r.mu.RLock()
	chain := append([]Middleware(nil), r.middleware[stage]...)
	r.mu.RUnlock()

	handler := terminal
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i](handler)
		if handler == nil {
			return fmt.Errorf("engine: middleware for stage %q returned a nil handler", stage)
		}
	}
	return handler(ctx, job)
}

// RegisterConnector registers a factory under name. Names are unique and
// registration is safe for concurrent use.
func (r *Registry) RegisterConnector(name string, factory ConnectorFactory) error {
	if name == "" {
		return errors.New("engine: connector name is empty")
	}
	if factory == nil {
		return fmt.Errorf("engine: connector %q has a nil factory", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = make(map[string]ConnectorFactory)
	}
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("engine: connector %q already registered", name)
	}
	r.factories[name] = factory
	return nil
}

// Connector returns the connector registered under name, constructed with a
// zero Config. It is meant for stateless connectors; use NewConnector when
// the factory needs indexer settings.
func (r *Registry) Connector(name string) (Connector, bool) {
	factory, ok := r.Factory(name)
	if !ok {
		return nil, false
	}
	connector, err := factory(Config{})
	if err != nil || connector == nil {
		return nil, false
	}
	return connector, true
}

// NewConnector builds the connector registered under name with cfg.
func (r *Registry) NewConnector(name string, cfg Config) (Connector, error) {
	factory, ok := r.Factory(name)
	if !ok {
		return nil, fmt.Errorf("engine: connector %q not found", name)
	}
	connector, err := factory(cfg)
	if err != nil {
		return nil, fmt.Errorf("engine: build connector %q: %w", name, err)
	}
	if connector == nil {
		return nil, fmt.Errorf("engine: connector %q factory returned nil", name)
	}
	return connector, nil
}

// Factory returns the factory registered under name.
func (r *Registry) Factory(name string) (ConnectorFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	return factory, ok
}

// Migration is one numbered schema migration declared by a module.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Module is a feature that plugs into the kernel at compile time. A disabled
// module is simply never registered, so it has no effect on schema or output.
type Module interface {
	Name() string
	Migrations() []Migration
	Register(r *Registry) error
}

// RegisterModule registers m after checking its name. The name is held while
// Register runs so concurrent registration of the same module fails cleanly;
// it is released again if Register returns an error.
func (r *Registry) RegisterModule(m Module) error {
	if m == nil {
		return errors.New("engine: nil module")
	}
	name := m.Name()
	if name == "" {
		return errors.New("engine: module name is empty")
	}

	r.mu.Lock()
	if r.modules == nil {
		r.modules = make(map[string]struct{})
	}
	if _, exists := r.modules[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("engine: module %q already registered", name)
	}
	r.modules[name] = struct{}{}
	r.mu.Unlock()

	if err := m.Register(r); err != nil {
		r.mu.Lock()
		delete(r.modules, name)
		r.mu.Unlock()
		return fmt.Errorf("engine: register module %q: %w", name, err)
	}
	return nil
}
