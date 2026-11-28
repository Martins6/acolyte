package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/martins6/acolyte/internal/board"
	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
)

// resolveBoardActor resolves who is acting on the board. It never
// authenticates: --as is attribution, and the registry is the only source of
// agent names.
func resolveBoardActor(agentName, workspaceFolder, as string) (board.Actor, error) {
	agentName = strings.TrimSpace(agentName)
	workspaceFolder = strings.TrimSpace(workspaceFolder)
	as = strings.TrimSpace(as)

	if as != "" && (agentName != "" || workspaceFolder != "") {
		return board.Actor{}, fmt.Errorf("use --as or --agent-name/--workspace-folder, not both")
	}
	if agentName != "" && workspaceFolder != "" {
		return board.Actor{}, fmt.Errorf("use --agent-name or --workspace-folder, not both")
	}
	if strings.EqualFold(as, board.UserActorID) {
		return board.UserActor(), nil
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		return board.Actor{}, err
	}
	defer reg.Close()

	ref := as
	if ref == "" {
		ref = agentName
	}
	if ref == "" {
		ref = workspaceFolder
	}
	if ref != "" {
		agent, err := reg.Resolve(ref)
		if err != nil {
			return board.Actor{}, fmt.Errorf("agent %q is not registered; pass --agent-name or --workspace-folder", ref)
		}
		return board.AgentActor(agent.Name), nil
	}

	if env := strings.TrimSpace(os.Getenv(paths.WorkspaceEnvVar)); env != "" {
		agent, err := reg.Resolve(env)
		if err != nil {
			return board.Actor{}, fmt.Errorf("workspace %q from %s is not registered", env, paths.WorkspaceEnvVar)
		}
		return board.AgentActor(agent.Name), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return board.Actor{}, fmt.Errorf("board: resolve current directory: %w", err)
	}
	dir, err := paths.Canonical(cwd)
	if err != nil {
		return board.Actor{}, err
	}
	for {
		if agent, err := reg.GetByWorkspace(dir); err == nil {
			return board.AgentActor(agent.Name), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return board.Actor{}, fmt.Errorf("agent not recognized; run inside a registered workspace or pass --agent-name/--workspace-folder")
}
