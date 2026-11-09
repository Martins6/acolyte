package cron

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MaxShellTimeout bounds shell job timeouts so seconds cannot overflow a
// time.Duration during conversion.
const MaxShellTimeout = 24 * time.Hour

type jobMeta struct {
	OnSuccess      string `json:"on_success"`
	OnFailure      string `json:"on_failure"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func parseJobMeta(raw string) (jobMeta, error) {
	var meta jobMeta
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			return jobMeta{}, fmt.Errorf("cron: parse job metadata: %w", err)
		}
	}
	meta.OnSuccess = strings.TrimSpace(meta.OnSuccess)
	meta.OnFailure = strings.TrimSpace(meta.OnFailure)
	if meta.TimeoutSeconds <= 0 {
		meta.TimeoutSeconds = int(DefaultShellTimeout / time.Second)
	}
	maxSeconds := int(MaxShellTimeout / time.Second)
	if meta.TimeoutSeconds > maxSeconds {
		meta.TimeoutSeconds = maxSeconds
	}
	return meta, nil
}
