package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONField stores arbitrary JSON (map[string]any) in a TEXT column.
// It implements driver.Valuer and sql.Scanner for GORM compatibility.
type JSONField map[string]any

// Value marshals the map to a JSON string for storage. A nil map produces
// a SQL NULL.
func (j JSONField) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, fmt.Errorf("marshal JSONField: %w", err)
	}
	return string(b), nil
}

// Scan unmarshals a JSON string (or []byte) from the database into the map.
// NULL values result in a nil map.
//
// If the stored JSON is an array rather than an object, Scan wraps it as
// {"items": [...]}) so callers always receive a map. This tolerates planners
// that emit bare arrays for fields like tdd_exceptions.
func (j *JSONField) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("JSONField.Scan: unsupported type %T", value)
	}
	m := make(map[string]any)
	if err := json.Unmarshal(data, &m); err != nil {
		// The stored value might be a JSON array; try unmarshaling as []any
		// and wrap in a map so the caller always gets map[string]any.
		var arr []any
		if arrErr := json.Unmarshal(data, &arr); arrErr != nil {
			return fmt.Errorf("unmarshal JSONField: %w", err)
		}
		*j = map[string]any{"items": arr}
		return nil
	}
	*j = m
	return nil
}

// JSONArray stores a JSON string array in a TEXT column.
// It implements driver.Valuer and sql.Scanner for GORM compatibility.
type JSONArray []string

// Value marshals the slice to a JSON string for storage. A nil slice
// produces a SQL NULL; an empty slice produces "[]".
func (j JSONArray) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, fmt.Errorf("marshal JSONArray: %w", err)
	}
	return string(b), nil
}

// Scan unmarshals a JSON string (or []byte) from the database into the slice.
// NULL values result in a nil slice.
//
// If the stored JSON contains numbers instead of strings (e.g. [0, 1] instead
// of ["0", "1"]), Scan converts each element to its string representation.
// This tolerates planners that emit numeric indices for tests_for.
func (j *JSONArray) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("JSONArray.Scan: unsupported type %T", value)
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		// Elements might be numbers or mixed types; unmarshal as []any and
		// convert each element to a string so callers always get []string.
		var raw []any
		if rawErr := json.Unmarshal(data, &raw); rawErr != nil {
			return fmt.Errorf("unmarshal JSONArray: %w", err)
		}
		result := make([]string, len(raw))
		for i, elem := range raw {
			switch v := elem.(type) {
			case string:
				result[i] = v
			case float64:
				// Emit integer form when possible (e.g. 2 → "2" not "2.000000").
				if v == float64(int64(v)) {
					result[i] = fmt.Sprintf("%d", int64(v))
				} else {
					result[i] = fmt.Sprintf("%g", v)
				}
			default:
				result[i] = fmt.Sprintf("%v", v)
			}
		}
		*j = result
		return nil
	}
	*j = arr
	return nil
}
