package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/routine"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

func (s *Service) runRoutine(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, bool, error) {
	if s.routines == nil {
		return "", "", false, fmt.Errorf("cron: routine registry is not configured")
	}
	routineValue, ok := s.routines.Get(job.Routine)
	if !ok {
		return "", "", false, fmt.Errorf("cron: routine %q is not registered", job.Routine)
	}

	args, err := routine.DecodeArgs(job.RoutineArgs)
	if err != nil {
		return "", "", false, fmt.Errorf("cron: parse routine args for %s: %w", job.Title, err)
	}
	normalized, err := routine.ValidateArgs(routineValue.Args(), args)
	if err != nil {
		return "", "", false, err
	}

	// Routines are headless and own their session. A stored session id is a
	// legacy or misconfigured value; never route a routine turn into a user
	// session, even if one was persisted on the row.
	thread := cronThread(job)
	if stored := strings.TrimSpace(job.SessionID); stored != "" {
		log.Printf("cron: routine %s ignores stored session %q; using %s", job.Title, stored, thread)
	}
	sessionID := ""
	session, ok, err := st.Orch().GetSessionByThread(thread)
	if err != nil {
		return "", "", false, err
	}
	if ok {
		sessionID = session.OpenCodeSessionID
	}

	jobMeta := map[string]any{}
	if strings.TrimSpace(job.Meta) != "" {
		_ = json.Unmarshal([]byte(job.Meta), &jobMeta)
	}

	admitted := false
	pendingGeneration := ""
	env := routine.Env{
		Agent:     agent,
		Workspace: agent.Workspace,
		Thread:    thread,
		SessionID: sessionID,
		Meta:      jobMeta,
		Now:       s.now,
		Store:     st,
		OnAdmitted: func(admittedTurn opencode.Admitted) error {
			// The callback means OpenCode accepted the input even if the marker
			// write below fails, so the run is never misclassified as
			// pre-admission.
			admitted = true
			if err := persistAdmittedPending(st, job.ID, admittedTurn.SessionID, admittedTurn.InputID, "routine", s.now()); err != nil {
				_ = st.Orch().SetCronJobPending(job.ID, "", cronPendingToken(), "indeterminate", s.now())
				return err
			}
			pendingGeneration = admittedTurn.InputID
			return nil
		},
		OnCollected: func(*opencode.TurnResult) error {
			// Consume the turn marker while the session lock is held, but keep
			// a run-level pending state: the routine body still has post-turn
			// work (audit, writer turn, delivery) and must be settled by
			// recovery if the process stops before it finishes.
			token := cronPendingToken()
			if err := st.Orch().SetCronJobPending(job.ID, "", token, "routine-run", s.now()); err != nil {
				return err
			}
			pendingGeneration = token
			return nil
		},
		NotifyUser: func(ctx context.Context, text string) error {
			return s.sink.NotifyUser(ctx, agent, text)
		},
		Inbox: func(ctx context.Context, text string, meta map[string]any) (string, string, error) {
			reply, inboxSession, sinkErr := s.sink.Inbox(ctx, agent, sessionID, text, meta)
			if inboxSession != "" {
				sessionID = inboxSession
				if _, err := st.Orch().UpsertSession(thread, inboxSession, s.now()); err != nil && sinkErr == nil {
					sinkErr = err
				}
			}
			return reply, inboxSession, sinkErr
		},
		InboxSession: func(ctx context.Context, targetSessionID, text string, meta map[string]any) (string, string, error) {
			return s.sink.Inbox(ctx, agent, targetSessionID, text, meta)
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
	if err == nil && pendingGeneration != "" {
		cleared, clearErr := st.Orch().ClearCronJobPendingIf(job.ID, pendingGeneration)
		if clearErr != nil {
			s.logJob(st, job, "warn", "routine completed but pending state could not be cleared: "+clearErr.Error())
		} else if !cleared {
			s.logJob(st, job, "warn", "routine completed but a newer run owns the pending state")
		}
	}
	return out, sessionID, admitted, err
}
