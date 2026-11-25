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

func (s *Service) runPrompt(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, bool, error) {
	if s.turn == nil {
		return "", "", false, fmt.Errorf("cron: OpenCode turn runner is not configured")
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return "", "", false, fmt.Errorf("cron: prompt is empty")
	}

	thread := cronThread(job)
	sessionID := job.SessionID
	if sessionID == "" {
		session, ok, err := st.Orch().GetSessionByThread(thread)
		if err != nil {
			return "", "", false, err
		}
		if ok {
			sessionID = session.OpenCodeSessionID
		}
	}

	admitted := false
	result, err := s.turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        job.Prompt,
		Description: "Scheduled prompt: " + job.Title,
		Metadata:    map[string]any{"cron_job": job.Title},
	}, opencode.TurnConfig{
		Workspace: agent.Workspace,
		OnAdmitted: func(admittedTurn opencode.Admitted) error {
			if err := st.Orch().SetCronJobPending(job.ID, admittedTurn.SessionID, admittedTurn.InputID, "turn", s.now()); err != nil {
				return err
			}
			admitted = true
			return nil
		},
	})
	if err != nil {
		created := ""
		if result != nil {
			created = result.SessionID
		}
		if created != "" {
			_, _ = st.Orch().UpsertSession(thread, created, s.now())
		}
		return "", created, admitted, err
	}
	// The turn was collected, so recovery must not collect it again even if
	// persisting the session mapping below fails.
	_ = st.Orch().ClearCronJobPending(job.ID)
	if _, err := st.Orch().UpsertSession(thread, result.SessionID, s.now()); err != nil {
		return result.ResponseText, result.SessionID, admitted, err
	}
	return result.ResponseText, result.SessionID, admitted, nil
}

func cronThread(job orch.CronJob) string {
	return "cron:" + job.ID
}
