package routine

import (
	"encoding/json"
	"fmt"
	"math"
)

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
				out[spec.Name] = spec.Default
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
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return boundedInt(parsed)
	default:
		return 0, false
	}
}

func boundedInt(value int64) (int, bool) {
	bits := int64(^uint(0) >> 1)
	if value > bits || value < -bits-1 {
		return 0, false
	}
	return int(value), true
}
