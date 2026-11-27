package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/martins6/acolyte/internal/paths"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
)

// resolveTargetAgent resolves --agent-name xor --workspace-folder, falling back
// to ACOLYTE_WORKSPACE_PATH and then the current directory.
func resolveTargetAgent(agentName, workspaceFolder string) (registry.Agent, error) {
	agentName = strings.TrimSpace(agentName)
	workspaceFolder = strings.TrimSpace(workspaceFolder)
	if agentName != "" && workspaceFolder != "" {
		return registry.Agent{}, fmt.Errorf("use --agent-name or --workspace-folder, not both")
	}

	ref := agentName
	if ref == "" {
		ref = workspaceFolder
	}
	if ref == "" {
		resolved, err := paths.ResolveWorkspace("")
		if err != nil {
			return registry.Agent{}, err
		}
		ref = resolved
	}

	reg, err := registry.OpenDefault()
	if err != nil {
		return registry.Agent{}, err
	}
	defer reg.Close()

	agent, err := reg.Resolve(ref)
	if errors.Is(err, registry.ErrNotFound) {
		return registry.Agent{}, fmt.Errorf("agent %q is not registered; use --agent-name or --workspace-folder", ref)
	}
	if err != nil {
		return registry.Agent{}, err
	}
	return agent, nil
}

// verifySessionMapping rejects an explicit session that is not mapped to the
// target agent's per-agent store. OpenCode sessions are global, so this is the
// ownership check that keeps an inbox/cron event inside the selected agent.
func verifySessionMapping(st *store.Store, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q is not mapped to this agent; omit --session-id or use a session created by this agent", sessionID)
	}
	return nil
}

// verifyPromotionSession is verifySessionMapping plus a main-session check:
// promotion must not run in a dedicated board automation or routine session.
func verifyPromotionSession(st *store.Store, sessionID string) error {
	if err := verifySessionMapping(st, sessionID); err != nil {
		return err
	}
	session, ok, err := st.Orch().GetSessionByOpenCodeID(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q is not mapped to this agent", sessionID)
	}
	switch {
	case strings.HasPrefix(session.Thread, "board:"):
		return fmt.Errorf("session %q is a board automation session; promotion needs a main session", sessionID)
	case strings.HasPrefix(session.Thread, "cron:"), strings.HasPrefix(session.Thread, "heartbeat:"):
		return fmt.Errorf("session %q is a routine session; promotion needs a main session", sessionID)
	}
	return nil
}
