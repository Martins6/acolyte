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

func (s *Service) runPrompt(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, bool, bool, string, error) {
	if s.turn == nil {
		return "", "", false, false, "", fmt.Errorf("cron: OpenCode turn runner is not configured")
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return "", "", false, false, "", fmt.Errorf("cron: prompt is empty")
	}

	thread := cronThread(job)
	sessionID := job.SessionID
	if sessionID == "" {
		session, ok, err := st.Orch().GetSessionByThread(thread)
		if err != nil {
			return "", "", false, false, "", err
		}
		if ok {
			sessionID = session.OpenCodeSessionID
		}
	}

	admitted := false
	collected := false
	routed := false
	generation := ""
	result, err := s.turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        job.Prompt,
		Description: "Scheduled prompt: " + job.Title,
		Metadata:    map[string]any{"cron_job": job.Title},
	}, opencode.TurnConfig{
		Workspace:     agent.Workspace,
		OnInteraction: cronInteractionLogger(agent.Name, st),
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
			collected = true
			generation = turn.InputID
			// Persist the route intent first, with a deterministic ID both live
			// execution and recovery derive. The marker is consumed only after
			// the intent exists, so a crash cannot lose or reroute the output.
			if err := s.addPromptRouteIntent(st, agent, job, turn.InputID, turn); err != nil {
				return err
			}
			if turn.SessionID != "" {
				if _, err := st.Orch().UpsertSession(cronThread(job), turn.SessionID, s.now()); err != nil {
					return err
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
			admitted = false
			routed = true
			generation = ""
			return nil
		},
	})
	if err != nil {
		if collected && result != nil {
			s.logJob(st, job, "warn", "turn collected but post-collection delivery failed: "+err.Error())
			if generation != "" {
				// The durable intent was not stored; keep the marker so
				// recovery retries instead of using a non-durable fallback.
				return result.ResponseText, result.SessionID, true, false, generation, err
			}
			return result.ResponseText, result.SessionID, false, true, "", nil
		}
		created := ""
		if result != nil {
			created = result.SessionID
		}
		if created != "" {
			_, _ = st.Orch().UpsertSession(thread, created, s.now())
		}
		return "", created, admitted, routed, generation, err
	}
	// The marker was consumed by OnCollected while the session lock was held.
	return result.ResponseText, result.SessionID, admitted, routed, generation, nil
}

func cronThread(job orch.CronJob) string {
	return "cron:" + job.ID
}

// cronInteractionLogger persists headless settlement outcomes as IDs-only
// orchestrator audit rows. A logging failure never changes the turn.
func cronInteractionLogger(agent string, st *store.Store) func(opencode.InteractionSettlement) {
	return func(settlement opencode.InteractionSettlement) {
		if st == nil {
			return
		}
		level := "info"
		if settlement.Err != nil {
			level = "warn"
		}
		_ = st.Orch().AppendLog(level, "opencode:"+agent,
			"headless "+settlement.Kind+" "+settlement.Action,
			opencode.SettlementAuditMeta(settlement), settlement.Time)
	}
}
