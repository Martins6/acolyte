package cron

import (
	"context"
	"fmt"
	"strings"

	"github.com/martins6/acolyte/internal/opencode"
	"github.com/martins6/acolyte/internal/registry"
	"github.com/martins6/acolyte/internal/store"
	"github.com/martins6/acolyte/internal/store/orch"
)

func (s *Service) runPrompt(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, bool, bool, error) {
	if s.turn == nil {
		return "", "", false, false, fmt.Errorf("cron: OpenCode turn runner is not configured")
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return "", "", false, false, fmt.Errorf("cron: prompt is empty")
	}

	thread := cronThread(job)
	sessionID := job.SessionID
	if sessionID == "" {
		session, ok, err := st.Orch().GetSessionByThread(thread)
		if err != nil {
			return "", "", false, false, err
		}
		if ok {
			sessionID = session.OpenCodeSessionID
		}
	}

	meta, _ := parseJobMeta(job.Meta)
	durableRoute := meta.OnSuccess == "" || meta.OnSuccess == "notify-user"
	admitted := false
	collected := false
	routed := false
	result, err := s.turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        job.Prompt,
		Description: "Scheduled prompt: " + job.Title,
		Metadata:    map[string]any{"cron_job": job.Title},
	}, opencode.TurnConfig{
		Workspace: agent.Workspace,
		OnAdmitted: func(admittedTurn opencode.Admitted) error {
			// The callback means OpenCode accepted the input even if the marker
			// write below fails, so the run is never misclassified as
			// pre-admission.
			admitted = true
			if err := persistAdmittedPending(st, job.ID, admittedTurn.SessionID, admittedTurn.InputID, "turn", s.now()); err != nil {
				// Keep a durable indeterminate marker so recovery settles the
				// run instead of routing a terminal failure while it runs.
				_ = st.Orch().SetCronJobPending(job.ID, "", cronPendingToken(), "indeterminate", s.now())
				return err
			}
			return nil
		},
		OnCollected: func(turn *opencode.TurnResult) error {
			// Persist the collected output first, with an ID both live execution
			// and recovery derive, then consume the marker. A crash between the
			// two therefore cannot lose the response.
			if durableRoute {
				var routeErr error
				routed, routeErr = s.enqueuePromptOutput(st, job, turn.InputID, turn)
				if routeErr != nil {
					return routeErr
				}
			}
			// Still holding the session lock here: consume the exact marker
			// before another process or recovery pass can see it.
			cleared, clearErr := st.Orch().ClearCronJobPendingIf(job.ID, turn.InputID)
			if clearErr != nil {
				return clearErr
			}
			if !cleared {
				return fmt.Errorf("cron: pending marker for %s was replaced", job.Title)
			}
			collected = true
			admitted = false
			return nil
		},
	})
	if err != nil {
		if collected && result != nil {
			// The output was collected and its marker consumed; the error is
			// post-collection cleanup (permission restore, lock release).
			// Route the real output instead of turning it into a failure.
			s.logJob(st, job, "warn", "turn collected but cleanup failed: "+err.Error())
			return result.ResponseText, result.SessionID, false, routed, nil
		}
		created := ""
		if result != nil {
			created = result.SessionID
		}
		if created != "" {
			_, _ = st.Orch().UpsertSession(thread, created, s.now())
		}
		return "", created, admitted, routed, err
	}
	// The marker was consumed by OnCollected while the session lock was held.
	if _, err := st.Orch().UpsertSession(thread, result.SessionID, s.now()); err != nil {
		// The output is already collected and will be routed; only the thread
		// mapping failed, so log it instead of turning a completed run into a
		// recoverable (and already cleared) one.
		s.logJob(st, job, "warn", "turn collected but session mapping could not be saved: "+err.Error())
	}
	return result.ResponseText, result.SessionID, false, routed, nil
}

func cronThread(job orch.CronJob) string {
	return "cron:" + job.ID
}
