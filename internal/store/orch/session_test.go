package orch

import (
	"testing"
	"time"
)

func TestGetTelegramSessionByOpenCodeID(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := store.UpsertSession("telegram:10", "ses_x", now); err != nil {
		t.Fatalf("UpsertSession(telegram) error: %v", err)
	}
	if _, err := store.UpsertSession("cron:job", "ses_x", now.Add(time.Minute)); err != nil {
		t.Fatalf("UpsertSession(cron) error: %v", err)
	}

	newest, ok, err := store.GetSessionByOpenCodeID("ses_x")
	if err != nil || !ok || newest.Thread != "cron:job" {
		t.Fatalf("newest mapping = %+v, %v, %v; want cron:job", newest, ok, err)
	}
	telegram, ok, err := store.GetTelegramSessionByOpenCodeID("ses_x")
	if err != nil || !ok || telegram.Thread != "telegram:10" {
		t.Errorf("telegram mapping = %+v, %v, %v; want telegram:10", telegram, ok, err)
	}
}
