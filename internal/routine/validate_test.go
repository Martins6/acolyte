package routine

import (
	"encoding/json"
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

func TestDecodeArgsPreservesLargeIntegers(t *testing.T) {
	args, err := DecodeArgs(`{"n":9007199254740993}`)
	if err != nil {
		t.Fatalf("DecodeArgs() error: %v", err)
	}
	raw, ok := args["n"].(json.Number)
	if !ok || raw.String() != "9007199254740993" {
		t.Fatalf("decoded n = %#v, want exact json.Number", args["n"])
	}
	normalized, err := ValidateArgs([]ArgSpec{{Name: "n", Type: "int"}}, args)
	if err != nil {
		t.Fatalf("ValidateArgs() error: %v", err)
	}
	if normalized["n"] != 9007199254740993 {
		t.Errorf("normalized n = %v, want 9007199254740993", normalized["n"])
	}
}

func TestDecodeArgsAcceptsIntegralDecimalsAndExponents(t *testing.T) {
	for raw, want := range map[string]int{
		`{"n":1.0}`:   1,
		`{"n":1e3}`:   1000,
		`{"n":1.5e2}`: 150,
		`{"n":-2.0}`:  -2,
	} {
		args, err := DecodeArgs(raw)
		if err != nil {
			t.Fatalf("DecodeArgs(%s) error: %v", raw, err)
		}
		normalized, err := ValidateArgs([]ArgSpec{{Name: "n", Type: "int"}}, args)
		if err != nil {
			t.Fatalf("ValidateArgs(%s) error: %v", raw, err)
		}
		if normalized["n"] != want {
			t.Errorf("ValidateArgs(%s) = %v, want %d", raw, normalized["n"], want)
		}
	}
	if _, err := ValidateArgs([]ArgSpec{{Name: "n", Type: "int"}}, map[string]any{"n": json.Number("1.2")}); err == nil {
		t.Error("ValidateArgs(1.2) error = nil, want error")
	}
}

func TestDecodeArgsRejectsInvalid(t *testing.T) {
	for _, raw := range []string{`{`, `{} {}`, `[]`} {
		if _, err := DecodeArgs(raw); err == nil {
			t.Errorf("DecodeArgs(%s) error = nil, want error", raw)
		}
	}
}

func TestValidateRejectsInvalidDefault(t *testing.T) {
	specs := []ArgSpec{{Name: "days", Type: "int", Default: "three"}}
	if _, err := ValidateArgs(specs, nil); err == nil {
		t.Fatal("ValidateArgs(invalid default) error = nil, want error")
	}
}

func TestValidateRequiredMissing(t *testing.T) {
	specs := []ArgSpec{{Name: "days", Type: "int", Required: true}}
	if _, err := ValidateArgs(specs, nil); err == nil {
		t.Fatal("ValidateArgs(missing required) error = nil, want error")
	}
}
