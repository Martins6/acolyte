package cron

import (
	"encoding/json"
	"strings"
	"time"
)

type jobMeta struct {
	OnSuccess      string `json:"on_success"`
	OnFailure      string `json:"on_failure"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func parseJobMeta(raw string) jobMeta {
	var meta jobMeta
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	meta.OnSuccess = strings.TrimSpace(meta.OnSuccess)
	meta.OnFailure = strings.TrimSpace(meta.OnFailure)
	if meta.TimeoutSeconds <= 0 {
		meta.TimeoutSeconds = int(DefaultShellTimeout / time.Second)
	}
	return meta
}
