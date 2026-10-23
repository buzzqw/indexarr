package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

type stubConnector struct{}

func (stubConnector) Capabilities() Capabilities { return Capabilities{Protocol: ProtocolTorrent} }
func (stubConnector) Search(context.Context, SearchRequest) ([]Item, error) {
	return nil, nil
}
func (stubConnector) Latest(context.Context, LatestRequest) ([]Item, error) { return nil, nil }
func (stubConnector) Download(context.Context, Item) (Payload, error)       { return Payload{}, nil }
func (stubConnector) Test(context.Context) (TestResult, error)              { return TestResult{OK: true}, nil }

func stubFactory(Config) (Connector, error) { return stubConnector{}, nil }

func TestInvokeMiddlewareOrder(t *testing.T) {
	registry := NewRegistry()
	var order []string

	recorder := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, job *Job) error {
				order = append(order, name+"-before")
				err := next(ctx, job)
				order = append(order, name+"-after")
				return err
			}
		}
	}
	registry.Use(StageRequest, recorder("first"))
	registry.Use(StageRequest, recorder("second"))

	err := registry.Invoke(context.Background(), StageRequest, &Job{}, func(context.Context, *Job) error {
		order = append(order, "terminal")
		return nil
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	want := []string{"first-before", "second-before", "terminal", "second-after", "first-after"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestInvokeShortCircuit(t *testing.T) {
	registry := NewRegistry()
	sentinel := errors.New("stop")
	terminalCalled := false

	registry.Use(StageFetch, func(Handler) Handler {
		return func(context.Context, *Job) error { return sentinel }
	})

	err := registry.Invoke(context.Background(), StageFetch, &Job{}, func(context.Context, *Job) error {
		terminalCalled = true
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Invoke error = %v, want %v", err, sentinel)
	}
	if err != sentinel {
		t.Errorf("middleware error was wrapped: %v", err)
	}
	if terminalCalled {
		t.Error("terminal ran after short-circuit")
	}
}

func TestInvokeStageIsolation(t *testing.T) {
	registry := NewRegistry()
	fetchCalled := false

	registry.Use(StageFetch, func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error {
			fetchCalled = true
			return next(ctx, job)
		}
	})

	terminalCalled := false
	err := registry.Invoke(context.Background(), StageExtract, &Job{}, func(context.Context, *Job) error {
		terminalCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if fetchCalled {
		t.Error("fetch middleware ran for the extract stage")
	}
	if !terminalCalled {
		t.Error("terminal did not run")
	}
}

func TestInvokeRejectsNilJobOrTerminal(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Invoke(context.Background(), StagePlan, nil, func(context.Context, *Job) error { return nil }); err == nil {
		t.Error("nil job accepted")
	}
	if err := registry.Invoke(context.Background(), StagePlan, &Job{}, nil); err == nil {
		t.Error("nil terminal accepted")
	}
}

func TestRegistryConnector(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterConnector("native", stubFactory); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}
	if err := registry.RegisterConnector("native", stubFactory); err == nil {
		t.Fatal("duplicate connector registration succeeded")
	}
	if err := registry.RegisterConnector("", stubFactory); err == nil {
		t.Error("empty connector name accepted")
	}
	if err := registry.RegisterConnector("nil", nil); err == nil {
		t.Error("nil factory accepted")
	}

	connector, ok := registry.Connector("native")
	if !ok || connector == nil {
		t.Fatal("Connector did not return the registered connector")
	}
	if _, ok := registry.Connector("missing"); ok {
		t.Error("unknown connector found")
	}

	built, err := registry.NewConnector("native", Config{Indexer: IndexerRef{Slug: "test"}})
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	if built == nil {
		t.Error("NewConnector returned nil")
	}
	if _, err := registry.NewConnector("missing", Config{}); err == nil {
		t.Error("NewConnector accepted an unknown name")
	}
}

func TestRegistryConnectorConcurrency(t *testing.T) {
	registry := NewRegistry()
	const (
		goroutines = 64
		names      = 8
	)
	var (
		wg      sync.WaitGroup
		success atomic.Int64
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("connector-%d", i%names)
			if err := registry.RegisterConnector(name, stubFactory); err == nil {
				success.Add(1)
			}
			_, _ = registry.Factory(name)
			_, _ = registry.Connector(name)
		}(i)
	}
	wg.Wait()

	if got := success.Load(); got != names {
		t.Errorf("successful registrations = %d, want %d", got, names)
	}
	for i := 0; i < names; i++ {
		if _, ok := registry.Connector(fmt.Sprintf("connector-%d", i)); !ok {
			t.Errorf("connector-%d was not registered", i)
		}
	}
}

type testModule struct {
	name        string
	registerErr error
	calls       int
}

func (m *testModule) Name() string { return m.name }
func (m *testModule) Migrations() []Migration {
	return []Migration{{Version: 1, Name: "module table", SQL: "CREATE TABLE module (id INTEGER)"}}
}
func (m *testModule) Register(registry *Registry) error {
	m.calls++
	if m.registerErr != nil {
		return m.registerErr
	}
	registry.Use(StageNormalize, func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error { return next(ctx, job) }
	})
	return nil
}

func TestRegisterModule(t *testing.T) {
	registry := NewRegistry()
	module := &testModule{name: "catalog"}

	if err := registry.RegisterModule(module); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := registry.RegisterModule(module); err == nil {
		t.Fatal("duplicate module registration succeeded")
	}
	if module.calls != 1 {
		t.Errorf("Register calls = %d, want 1", module.calls)
	}
	if err := registry.RegisterModule(nil); err == nil {
		t.Error("nil module accepted")
	}
	if err := registry.RegisterModule(&testModule{name: ""}); err == nil {
		t.Error("empty module name accepted")
	}
}

func TestRegisterModuleFailureReleasesName(t *testing.T) {
	registry := NewRegistry()
	module := &testModule{name: "failing", registerErr: errors.New("cannot register")}

	if err := registry.RegisterModule(module); err == nil {
		t.Fatal("failing module registration succeeded")
	}
	module.registerErr = nil
	if err := registry.RegisterModule(module); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
}
