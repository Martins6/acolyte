package routine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeRoutine struct {
	name  string
	owner string
	args  []ArgSpec
	run   func(context.Context, Env, map[string]any) (string, error)
}

func (f fakeRoutine) Name() string        { return f.name }
func (f fakeRoutine) Owner() string       { return f.owner }
func (f fakeRoutine) Description() string { return "fake" }
func (f fakeRoutine) Args() []ArgSpec     { return f.args }
func (f fakeRoutine) Run(ctx context.Context, env Env, args map[string]any) (string, error) {
	if f.run != nil {
		return f.run(ctx, env, args)
	}
	return "", nil
}

func TestRegistryRegisterAndList(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeRoutine{name: "zulu", owner: "test"}); err != nil {
		t.Fatalf("Register(zulu) error: %v", err)
	}
	if err := registry.Register(fakeRoutine{name: "alpha", owner: "test"}); err != nil {
		t.Fatalf("Register(alpha) error: %v", err)
	}
	if err := registry.Register(fakeRoutine{name: "alpha", owner: "test"}); err == nil {
		t.Fatal("Register(duplicate) error = nil, want error")
	}
	if err := registry.Register(fakeRoutine{name: "", owner: "test"}); err == nil {
		t.Fatal("Register(empty name) error = nil, want error")
	}

	list := registry.List()
	if len(list) != 2 || list[0].Name() != "alpha" || list[1].Name() != "zulu" {
		t.Errorf("List() = %+v, want alpha,zulu", list)
	}
	if _, ok := registry.Get("alpha"); !ok {
		t.Error("Get(alpha) not found")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Error("Get(missing) found")
	}
}

func TestRegisterRejectsDuplicateArgs(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(fakeRoutine{
		name:  "dup",
		owner: "test",
		args:  []ArgSpec{{Name: "days", Type: "int"}, {Name: "days", Type: "string"}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate argument") {
		t.Fatalf("Register(duplicate args) error = %v, want duplicate argument error", err)
	}
}

func TestCatalogJSON(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeRoutine{
		name:  "dream",
		owner: "memory-learn",
		args:  []ArgSpec{{Name: "days", Type: "int", Required: true, Default: 3}},
	}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	raw, err := json.Marshal(registry.Catalog())
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"name":"dream"`, `"owner":"memory-learn"`, `"args":[`, `"days"`} {
		if !strings.Contains(text, want) {
			t.Errorf("catalog %s missing %s", text, want)
		}
	}
}

func TestCatalogConcurrentRegister(t *testing.T) {
	registry := NewRegistry()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = registry.Register(fakeRoutine{name: "r" + string(rune('a'+i%26)), owner: "test"})
		}
	}()
	for i := 0; i < 50; i++ {
		_ = registry.Catalog()
	}
	<-done
}

func TestDefaultBuiltins(t *testing.T) {
	registry := Default()
	for _, name := range []string{"dream", "heartbeat"} {
		value, ok := registry.Get(name)
		if !ok {
			t.Fatalf("Default() missing %s", name)
		}
		if len(value.Args()) == 0 {
			t.Errorf("%s has no args", name)
		}
		_, err := value.Run(context.Background(), Env{}, nil)
		if !errors.Is(err, ErrPendingPhase4) {
			t.Errorf("%s Run() error = %v, want ErrPendingPhase4", name, err)
		}
	}
}
