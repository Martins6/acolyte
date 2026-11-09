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

func (s *Service) runPrompt(ctx context.Context, agent registry.Agent, st *store.Store, job orch.CronJob) (string, string, error) {
	if s.turn == nil {
		return "", "", fmt.Errorf("cron: OpenCode turn runner is not configured")
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return "", "", fmt.Errorf("cron: prompt is empty")
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

	result, err := s.turn.RunSyntheticTurn(ctx, sessionID, opencode.SyntheticEvent{
		Text:        job.Prompt,
		Description: "Scheduled prompt: " + job.Title,
		Metadata:    map[string]any{"cron_job": job.Title},
	}, opencode.TurnConfig{Workspace: agent.Workspace})
	if err != nil {
		created := ""
		if result != nil {
			created = result.SessionID
		}
		if created != "" {
			_, _ = st.Orch().UpsertSession(thread, created, s.now())
		}
		return "", created, err
	}
	if _, err := st.Orch().UpsertSession(thread, result.SessionID, s.now()); err != nil {
		return result.ResponseText, result.SessionID, err
	}
	return result.ResponseText, result.SessionID, nil
}

func cronThread(job orch.CronJob) string {
	return "cron:" + job.ID
}
