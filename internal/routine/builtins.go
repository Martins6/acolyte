package routine

import (
	"context"
	"errors"
	"fmt"
)

// ErrPendingPhase4 marks built-in routines whose real behavior lands in Phase 4.
var ErrPendingPhase4 = errors.New("routine: behavior pending phase 4")

type dreamRoutine struct{}

func (dreamRoutine) Name() string        { return "dream" }
func (dreamRoutine) Owner() string       { return "memory-learn" }
func (dreamRoutine) Description() string { return "Periodic memory distillation over recent sessions" }
func (dreamRoutine) Args() []ArgSpec {
	return []ArgSpec{{
		Name:        "days",
		Type:        "int",
		Required:    true,
		Default:     3,
		Description: "Run interval and lookback window in days",
	}}
}
func (dreamRoutine) Run(context.Context, Env, map[string]any) (string, error) {
	return "", fmt.Errorf("%w: dream", ErrPendingPhase4)
}

type heartbeatRoutine struct{}

func (heartbeatRoutine) Name() string  { return "heartbeat" }
func (heartbeatRoutine) Owner() string { return "heartbeat" }
func (heartbeatRoutine) Description() string {
	return "Periodic check for genuinely interesting things to surface"
}
func (heartbeatRoutine) Args() []ArgSpec {
	return []ArgSpec{{
		Name:        "interval",
		Type:        "int",
		Required:    true,
		Default:     60,
		Description: "Heartbeat interval in minutes",
	}}
}
func (heartbeatRoutine) Run(context.Context, Env, map[string]any) (string, error) {
	return "", fmt.Errorf("%w: heartbeat", ErrPendingPhase4)
}
