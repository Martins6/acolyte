package routine

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
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
		return parseJSONInteger(value.String())
	default:
		return 0, false
	}
}

// parseJSONInteger evaluates a JSON number exactly without constructing large
// powers of ten. Decimal/exponent forms are accepted only when the exact value
// is an integer that fits the platform int.
func parseJSONInteger(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 {
		return 0, false
	}

	negative := false
	if raw[0] == '-' {
		negative = true
		raw = raw[1:]
	}
	if raw == "" {
		return 0, false
	}

	exponent := int64(0)
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		parsed, err := strconv.ParseInt(raw[index+1:], 10, 64)
		if err != nil {
			return 0, false
		}
		exponent = parsed
		raw = raw[:index]
	}

	fractionLen := int64(0)
	if index := strings.IndexByte(raw, '.'); index >= 0 {
		fractionLen = int64(len(raw) - index - 1)
		raw = raw[:index] + raw[index+1:]
	}

	digits := strings.TrimLeft(raw, "0")
	if digits == "" {
		return 0, true
	}
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exponent++
	}
	exponent -= fractionLen
	if exponent < 0 {
		return 0, false
	}
	// 19 decimal digits is enough for uint64; the platform int bound is
	// checked after parsing.
	if int64(len(digits))+exponent > 19 {
		return 0, false
	}
	numeric := digits + strings.Repeat("0", int(exponent))
	value, err := strconv.ParseUint(numeric, 10, 64)
	if err != nil {
		return 0, false
	}

	maxInt := uint64(^uint(0) >> 1)
	if negative {
		if value > maxInt+1 {
			return 0, false
		}
		if value == maxInt+1 {
			return -int(maxInt) - 1, true
		}
		return -int(value), true
	}
	if value > maxInt {
		return 0, false
	}
	return int(value), true
}

func boundedInt(value int64) (int, bool) {
	bits := int64(^uint(0) >> 1)
	if value > bits || value < -bits-1 {
		return 0, false
	}
	return int(value), true
}
