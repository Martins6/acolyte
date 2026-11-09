package routine

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// DecodeArgs parses a routine argument object without losing integer precision.
func DecodeArgs(raw string) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("routine: parse args: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("routine: unexpected trailing args")
		}
		return nil, fmt.Errorf("routine: parse args: %w", err)
	}
	return args, nil
}

// ValidateArgs applies defaults, type-checks known args, and rejects unknown
// arguments. The returned map is the normalized execution argument set.
func ValidateArgs(specs []ArgSpec, args map[string]any) (map[string]any, error) {
	known := make(map[string]ArgSpec, len(specs))
	for _, spec := range specs {
		known[spec.Name] = spec
	}

	out := make(map[string]any, len(specs))
	for _, spec := range specs {
		raw, ok := args[spec.Name]
		if !ok {
			if spec.Required && spec.Default == nil {
				return nil, fmt.Errorf("routine: missing required argument %q", spec.Name)
			}
			if spec.Default != nil {
				value, err := normalizeValue(spec, spec.Default)
				if err != nil {
					return nil, fmt.Errorf("routine: invalid default for %q: %w", spec.Name, err)
				}
				out[spec.Name] = value
			}
			continue
		}
		value, err := normalizeValue(spec, raw)
		if err != nil {
			return nil, err
		}
		out[spec.Name] = value
	}

	for name := range args {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("routine: unknown argument %q", name)
		}
	}
	return out, nil
}

func normalizeValue(spec ArgSpec, raw any) (any, error) {
	switch spec.Type {
	case "int":
		value, ok := asInt(raw)
		if !ok {
			return nil, fmt.Errorf("routine: argument %q must be an integer", spec.Name)
		}
		return value, nil
	case "string":
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("routine: argument %q must be a string", spec.Name)
		}
		return value, nil
	case "bool":
		value, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("routine: argument %q must be a boolean", spec.Name)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("routine: argument %q has unsupported type %q", spec.Name, spec.Type)
	}
}

func asInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return boundedInt(value)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return 0, false
		}
		// float64(math.MaxInt64) rounds up to 2^63, so use an exclusive
		// upper bound and an inclusive lower bound before conversion.
		if value >= float64(math.MaxInt64) || value < float64(math.MinInt64) {
			return 0, false
		}
		return boundedInt(int64(value))
	case json.Number:
		raw := value.String()
		if parsed, err := value.Int64(); err == nil {
			return boundedInt(parsed)
		}
		if !safeNumberMagnitude(raw) {
			return 0, false
		}
		// Accept exact integral forms such as 1.0 and 1e3 without going
		// through float64 (which would round large integers).
		rat, ok := new(big.Rat).SetString(raw)
		if !ok || !rat.IsInt() || !rat.Num().IsInt64() {
			return 0, false
		}
		return boundedInt(rat.Num().Int64())
	default:
		return 0, false
	}
}

// safeNumberMagnitude rejects inputs that would make big.Rat allocate huge
// powers of ten before the int-range check can run.
func safeNumberMagnitude(raw string) bool {
	if len(raw) > 1024 {
		return false
	}
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		exponent, err := strconv.Atoi(raw[index+1:])
		if err != nil || exponent > 1000 || exponent < -1000 {
			return false
		}
	}
	return true
}

func boundedInt(value int64) (int, bool) {
	bits := int64(^uint(0) >> 1)
	if value > bits || value < -bits-1 {
		return 0, false
	}
	return int(value), true
}
