package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/martins6/acolyte/internal/paths"
)

func openTest(t *testing.T) *Registry {
	t.Helper()
	t.Setenv(paths.HomeEnv, t.TempDir())
	r, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault() error: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func testAgent(t *testing.T, name string) Agent {
	t.Helper()
	return Agent{
		Name:        name,
		Workspace:   t.TempDir(),
		Description: "test agent",
	}
}

func TestRegisterAndLookup(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")

	if err := r.Register(agent); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	byName, err := r.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName() error: %v", err)
	}
	wantWorkspace, err := paths.Canonical(agent.Workspace)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	if byName.Workspace != wantWorkspace {
		t.Errorf("workspace = %q, want %q", byName.Workspace, wantWorkspace)
	}
	if byName.Engine != "opencode" {
		t.Errorf("engine = %q, want opencode", byName.Engine)
	}
	if !byName.Enabled {
		t.Error("enabled = false, want true")
	}
	if byName.RegisteredAt.IsZero() || byName.LastSeenAt.IsZero() {
		t.Error("timestamps were not set")
	}

	byPath, err := r.GetByWorkspace(agent.Workspace)
	if err != nil {
		t.Fatalf("GetByWorkspace() error: %v", err)
	}
	if byPath.Name != "valet" {
		t.Errorf("name = %q, want valet", byPath.Name)
	}
}

func TestRegisterRejectsBlankIdentity(t *testing.T) {
	r := openTest(t)

	if err := r.Register(Agent{Name: "  ", Workspace: t.TempDir()}); err == nil {
		t.Error("Register(blank name) error = nil, want error")
	}
	if err := r.Register(Agent{Name: "valet", Workspace: "  "}); err == nil {
		t.Error("Register(blank workspace) error = nil, want error")
	}
	if err := r.Update(Agent{Name: "  "}); err == nil {
		t.Error("Update(blank name) error = nil, want error")
	}
	if _, err := r.GetByWorkspace("  "); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByWorkspace(blank) error = %v, want ErrNotFound", err)
	}
}

func TestRegisterNormalizesWorkspace(t *testing.T) {
	r := openTest(t)
	absolute := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	relative, err := filepath.Rel(cwd, absolute)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}

	if err := r.Register(Agent{Name: "valet", Workspace: relative}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	agent, err := r.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName() error: %v", err)
	}
	want, err := paths.Canonical(absolute)
	if err != nil {
		t.Fatalf("Canonical() error: %v", err)
	}
	if agent.Workspace != want {
		t.Errorf("workspace = %q, want %q", agent.Workspace, want)
	}
}

func TestGetByWorkspaceResolvesSymlink(t *testing.T) {
	r := openTest(t)
	workspace := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := r.Register(Agent{Name: "valet", Workspace: workspace}); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	agent, err := r.GetByWorkspace(link)
	if err != nil {
		t.Fatalf("GetByWorkspace(link) error: %v", err)
	}
	if agent.Name != "valet" {
		t.Errorf("name = %q, want valet", agent.Name)
	}
}

func TestRegisterAdoptsExistingIdentity(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register(first) error: %v", err)
	}

	agent.Description = "updated"
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register(adopt) error: %v", err)
	}
	got, err := r.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName() error: %v", err)
	}
	if got.Description != "updated" {
		t.Errorf("description = %q, want updated", got.Description)
	}
	agents, err := r.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("agents = %d, want 1", len(agents))
	}
}

func TestDuplicateName(t *testing.T) {
	r := openTest(t)
	first := testAgent(t, "valet")
	if err := r.Register(first); err != nil {
		t.Fatalf("Register(first) error: %v", err)
	}

	second := testAgent(t, "valet")
	if err := r.Register(second); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Register(duplicate name) error = %v, want ErrNameTaken", err)
	}
}

func TestDuplicateWorkspace(t *testing.T) {
	r := openTest(t)
	workspace := t.TempDir()

	if err := r.Register(Agent{Name: "one", Workspace: workspace}); err != nil {
		t.Fatalf("Register(one) error: %v", err)
	}
	if err := r.Register(Agent{Name: "two", Workspace: workspace}); !errors.Is(err, ErrWorkspaceTaken) {
		t.Fatalf("Register(duplicate workspace) error = %v, want ErrWorkspaceTaken", err)
	}
}

func TestUpdate(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	agent.Description = "updated"
	if err := r.Update(agent); err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	got, err := r.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName() error: %v", err)
	}
	if got.Description != "updated" {
		t.Errorf("description = %q, want updated", got.Description)
	}

	missing := testAgent(t, "missing")
	if err := r.Update(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrNotFound", err)
	}
}

func TestUpdateRequiresExactIdentity(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	agent.Workspace = t.TempDir()
	agent.Description = "replacement"
	if err := r.Update(agent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(different workspace) error = %v, want ErrNotFound", err)
	}
	got, err := r.GetByName("valet")
	if err != nil {
		t.Fatalf("GetByName() error: %v", err)
	}
	if got.Description == "replacement" {
		t.Error("Update mutated an agent with a different workspace")
	}
}

func TestResolve(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	if got, err := r.Resolve("valet"); err != nil || got.Name != "valet" {
		t.Errorf("Resolve(name) = %q, %v; want valet, nil", got.Name, err)
	}
	if got, err := r.Resolve(agent.Workspace); err != nil || got.Name != "valet" {
		t.Errorf("Resolve(path) = %q, %v; want valet, nil", got.Name, err)
	}
	if _, err := r.Resolve("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(missing) error = %v, want ErrNotFound", err)
	}
}

func TestListOrder(t *testing.T) {
	r := openTest(t)
	for _, name := range []string{"zulu", "alpha"} {
		if err := r.Register(testAgent(t, name)); err != nil {
			t.Fatalf("Register(%s) error: %v", name, err)
		}
	}

	agents, err := r.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(agents))
	}
	if agents[0].Name != "alpha" || agents[1].Name != "zulu" {
		t.Errorf("order = %q, %q; want alpha, zulu", agents[0].Name, agents[1].Name)
	}
}

func TestDeleteLeavesWorkspace(t *testing.T) {
	r := openTest(t)
	agent := testAgent(t, "valet")
	if err := r.Register(agent); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	deleted, err := r.Delete("valet")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if deleted.Name != "valet" {
		t.Errorf("deleted name = %q, want valet", deleted.Name)
	}
	if _, err := r.GetByName("valet"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByName() error = %v, want ErrNotFound", err)
	}
	if info, err := os.Stat(agent.Workspace); err != nil || !info.IsDir() {
		t.Errorf("workspace was removed or unreadable: %v", err)
	}

	if _, err := r.Delete(agent.Workspace); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) error = %v, want ErrNotFound", err)
	}
}

func TestRegistryFilePermissions(t *testing.T) {
	r := openTest(t)
	info, err := os.Stat(r.Path())
	if err != nil {
		t.Fatalf("stat registry: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("registry mode = %o, want 0600", perm)
	}
}

func TestOpenCreatesNestedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "registry.sqlite.db")
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer r.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat registry: %v", err)
	}
}
