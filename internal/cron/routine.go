package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

func (s *Service) runRoutine(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, error) {
	if s.routines == nil {
		return "", "", fmt.Errorf("cron: routine registry is not configured")
	}
	routineValue, ok := s.routines.Get(job.Routine)
	if !ok {
		return "", "", fmt.Errorf("cron: routine %q is not registered", job.Routine)
	}

	args := map[string]any{}
	if strings.TrimSpace(job.RoutineArgs) != "" {
		if err := json.Unmarshal([]byte(job.RoutineArgs), &args); err != nil {
			return "", "", fmt.Errorf("cron: parse routine args for %s: %w", job.Title, err)
		}
	}
	normalized, err := routine.ValidateArgs(routineValue.Args(), args)
	if err != nil {
		return "", "", err
	}

	thread := cronThread(job)
	sessionID := job.SessionID
	if sessionID == "" {
		session, ok, err := st.Orch().GetSessionByThread(thread)
		if err != nil {
			return "", "", err
		}
		if ok {
			sessionID = session.OpenCodeSessionID
		}
	}

	env := routine.Env{
		Agent:     agent,
		Workspace: agent.Workspace,
		SessionID: sessionID,
		Store:     st,
		NotifyUser: func(ctx context.Context, text string) error {
			return s.sink.NotifyUser(ctx, agent, text)
		},
		Inbox: func(ctx context.Context, text string, meta map[string]any) (string, string, error) {
			reply, inboxSession, sinkErr := s.sink.Inbox(ctx, agent, sessionID, text, meta)
			if inboxSession != "" {
				if _, err := st.Orch().UpsertSession(thread, inboxSession, s.now()); err != nil && sinkErr == nil {
					sinkErr = err
				}
			}
			return reply, inboxSession, sinkErr
		},
		Log: func(level, message string, meta map[string]any) {
			raw, _ := json.Marshal(meta)
			_ = st.Orch().AppendLog(level, "routine:"+routineValue.Name(), message, string(raw), s.now())
		},
	}
	if client, ok := s.turn.(*opencode.Client); ok {
		env.OpenCode = client
	}

	out, err := routineValue.Run(ctx, env, normalized)
	return out, sessionID, err
}
