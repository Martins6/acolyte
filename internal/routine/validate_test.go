package routine

import (
	"math"
	"testing"
)

func TestValidateArgs(t *testing.T) {
	specs := []ArgSpec{
		{Name: "days", Type: "int", Required: true, Default: 3},
		{Name: "label", Type: "string"},
		{Name: "enabled", Type: "bool"},
	}

	got, err := ValidateArgs(specs, nil)
	if err != nil {
		t.Fatalf("ValidateArgs(nil) error: %v", err)
	}
	if got["days"] != 3 {
		t.Errorf("days = %v, want default 3", got["days"])
	}

	got, err = ValidateArgs(specs, map[string]any{"days": float64(5), "label": "x", "enabled": true})
	if err != nil {
		t.Fatalf("ValidateArgs(full) error: %v", err)
	}
	if got["days"] != 5 || got["label"] != "x" || got["enabled"] != true {
		t.Errorf("args = %+v", got)
	}

	for _, args := range []map[string]any{
		{"days": "3"},
		{"days": 3, "unknown": 1},
		{"days": 3.5},
		{"days": math.Pow(2, 63)},
	} {
		if _, err := ValidateArgs(specs, args); err == nil {
			t.Errorf("ValidateArgs(%+v) error = nil, want error", args)
		}
	}
}

func TestValidateRequiredMissing(t *testing.T) {
	specs := []ArgSpec{{Name: "days", Type: "int", Required: true}}
	if _, err := ValidateArgs(specs, nil); err == nil {
		t.Fatal("ValidateArgs(missing required) error = nil, want error")
	}
}
