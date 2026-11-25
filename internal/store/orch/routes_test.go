package orch

import (
	"testing"
	"time"
)

func TestCronRouteActionAndAdmission(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	route := CronRoute{
		ID:          "route_1",
		AgentTarget: "valet",
		Action:      "inbox",
		JobID:       "job_1",
		Text:        "hello",
		Meta:        `{"cron_job":"report"}`,
		CreatedAt:   now,
	}
	if err := store.AddCronRoute(route); err != nil {
		t.Fatalf("AddCronRoute() error: %v", err)
	}

	routes, err := store.DueCronRoutes(now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("DueCronRoutes() error: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want one", routes)
	}
	got := routes[0]
	if got.Action != "inbox" || got.JobID != "job_1" || got.ChatID != 0 || got.Meta != route.Meta {
		t.Errorf("route = %+v, want action/job/meta preserved", got)
	}

	if err := store.SetCronRouteAdmitted("route_1", "ses_1", "msg_1"); err != nil {
		t.Fatalf("SetCronRouteAdmitted() error: %v", err)
	}
	routes, err = store.DueCronRoutes(now.Add(time.Second), 10)
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes after admission = %+v, %v", routes, err)
	}
	if routes[0].OpenCodeSessionID != "ses_1" || routes[0].SourceMessageID != "msg_1" {
		t.Errorf("admission = %q/%q, want ses_1/msg_1", routes[0].OpenCodeSessionID, routes[0].SourceMessageID)
	}

	if err := store.MarkCronRouteDelivered("route_1", now); err != nil {
		t.Fatalf("MarkCronRouteDelivered() error: %v", err)
	}
	routes, err = store.DueCronRoutes(now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("DueCronRoutes() error: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want none after delivery", routes)
	}
}
