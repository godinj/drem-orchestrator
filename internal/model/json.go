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
		return fmt.Errorf("unmarshal JSONField: %w", err)
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
		return fmt.Errorf("unmarshal JSONArray: %w", err)
	}
	*j = arr
	return nil
}
