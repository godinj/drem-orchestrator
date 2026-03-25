package model

import (
	"strings"
	"testing"
)

func TestJSONFieldScan(t *testing.T) {
	t.Run("unsupported type", func(t *testing.T) {
		var j JSONField
		err := j.Scan(123)
		if err == nil {
			t.Fatal("expected error for int input, got nil")
		}
		if !strings.Contains(err.Error(), "unsupported type") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "unsupported type")
		}
	})

	t.Run("malformed JSON string", func(t *testing.T) {
		var j JSONField
		err := j.Scan("{broken}")
		if err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
		if !strings.Contains(err.Error(), "unmarshal") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "unmarshal")
		}
	})

	t.Run("nil value", func(t *testing.T) {
		var j JSONField
		err := j.Scan(nil)
		if err != nil {
			t.Fatalf("Scan(nil) returned error: %v", err)
		}
		if j != nil {
			t.Errorf("expected nil map, got %v", j)
		}
	})

	t.Run("byte slice", func(t *testing.T) {
		var j JSONField
		err := j.Scan([]byte(`{"key":"value"}`))
		if err != nil {
			t.Fatalf("Scan([]byte) returned error: %v", err)
		}
		if j == nil {
			t.Fatal("expected non-nil map, got nil")
		}
		if j["key"] != "value" {
			t.Errorf("j[\"key\"] = %v, want %q", j["key"], "value")
		}
	})

	t.Run("string", func(t *testing.T) {
		var j JSONField
		err := j.Scan(`{"a":"b","c":"d"}`)
		if err != nil {
			t.Fatalf("Scan(string) returned error: %v", err)
		}
		if j["a"] != "b" {
			t.Errorf("j[\"a\"] = %v, want %q", j["a"], "b")
		}
		if j["c"] != "d" {
			t.Errorf("j[\"c\"] = %v, want %q", j["c"], "d")
		}
	})
}

func TestJSONFieldScanArray(t *testing.T) {
	t.Run("bare array wraps in items key", func(t *testing.T) {
		var j JSONField
		err := j.Scan(`[{"subtask_index":2,"reason":"Integration wiring only"}]`)
		if err != nil {
			t.Fatalf("Scan(array) returned error: %v", err)
		}
		items, ok := j["items"]
		if !ok {
			t.Fatal("expected 'items' key in wrapped map")
		}
		arr, ok := items.([]any)
		if !ok {
			t.Fatalf("items is %T, want []any", items)
		}
		if len(arr) != 1 {
			t.Fatalf("items length = %d, want 1", len(arr))
		}
		elem, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("arr[0] is %T, want map[string]any", arr[0])
		}
		if elem["reason"] != "Integration wiring only" {
			t.Errorf("reason = %v, want %q", elem["reason"], "Integration wiring only")
		}
	})

	t.Run("empty array wraps in items key", func(t *testing.T) {
		var j JSONField
		err := j.Scan(`[]`)
		if err != nil {
			t.Fatalf("Scan(empty array) returned error: %v", err)
		}
		items, ok := j["items"]
		if !ok {
			t.Fatal("expected 'items' key")
		}
		arr, ok := items.([]any)
		if !ok {
			t.Fatalf("items is %T, want []any", items)
		}
		if len(arr) != 0 {
			t.Errorf("items length = %d, want 0", len(arr))
		}
	})

	t.Run("byte slice array wraps in items key", func(t *testing.T) {
		var j JSONField
		err := j.Scan([]byte(`[1, 2, 3]`))
		if err != nil {
			t.Fatalf("Scan([]byte array) returned error: %v", err)
		}
		if _, ok := j["items"]; !ok {
			t.Fatal("expected 'items' key")
		}
	})

	t.Run("truly malformed JSON still errors", func(t *testing.T) {
		var j JSONField
		err := j.Scan(`not json at all`)
		if err == nil {
			t.Fatal("expected error for non-JSON input")
		}
	})
}

func TestJSONFieldValue(t *testing.T) {
	t.Run("nil map", func(t *testing.T) {
		var j JSONField
		val, err := j.Value()
		if err != nil {
			t.Fatalf("Value() returned error: %v", err)
		}
		if val != nil {
			t.Errorf("expected nil, got %v", val)
		}
	})

	t.Run("valid map", func(t *testing.T) {
		j := JSONField{"key": "value"}
		val, err := j.Value()
		if err != nil {
			t.Fatalf("Value() returned error: %v", err)
		}
		s, ok := val.(string)
		if !ok {
			t.Fatalf("expected string, got %T", val)
		}
		if !strings.Contains(s, `"key"`) || !strings.Contains(s, `"value"`) {
			t.Errorf("Value() = %q, want JSON containing key and value", s)
		}
	})
}

func TestJSONArrayScan(t *testing.T) {
	t.Run("unsupported type", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(123)
		if err == nil {
			t.Fatal("expected error for int input, got nil")
		}
		if !strings.Contains(err.Error(), "unsupported type") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "unsupported type")
		}
	})

	t.Run("malformed JSON string", func(t *testing.T) {
		var j JSONArray
		err := j.Scan("[broken]")
		if err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
		if !strings.Contains(err.Error(), "unmarshal") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "unmarshal")
		}
	})

	t.Run("nil value", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(nil)
		if err != nil {
			t.Fatalf("Scan(nil) returned error: %v", err)
		}
		if j != nil {
			t.Errorf("expected nil slice, got %v", j)
		}
	})

	t.Run("byte slice", func(t *testing.T) {
		var j JSONArray
		err := j.Scan([]byte(`["alpha","beta"]`))
		if err != nil {
			t.Fatalf("Scan([]byte) returned error: %v", err)
		}
		if len(j) != 2 {
			t.Fatalf("len = %d, want 2", len(j))
		}
		if j[0] != "alpha" {
			t.Errorf("j[0] = %q, want %q", j[0], "alpha")
		}
		if j[1] != "beta" {
			t.Errorf("j[1] = %q, want %q", j[1], "beta")
		}
	})

	t.Run("string", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(`["x","y","z"]`)
		if err != nil {
			t.Fatalf("Scan(string) returned error: %v", err)
		}
		if len(j) != 3 {
			t.Fatalf("len = %d, want 3", len(j))
		}
		if j[0] != "x" || j[1] != "y" || j[2] != "z" {
			t.Errorf("j = %v, want [x y z]", j)
		}
	})
}

func TestJSONArrayScanNumbers(t *testing.T) {
	t.Run("numeric array converts to strings", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(`[0, 1, 2]`)
		if err != nil {
			t.Fatalf("Scan(numbers) returned error: %v", err)
		}
		if len(j) != 3 {
			t.Fatalf("len = %d, want 3", len(j))
		}
		if j[0] != "0" || j[1] != "1" || j[2] != "2" {
			t.Errorf("j = %v, want [0 1 2]", j)
		}
	})

	t.Run("mixed string and number array", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(`["a", 1, "b", 2]`)
		if err != nil {
			t.Fatalf("Scan(mixed) returned error: %v", err)
		}
		if len(j) != 4 {
			t.Fatalf("len = %d, want 4", len(j))
		}
		if j[0] != "a" || j[1] != "1" || j[2] != "b" || j[3] != "2" {
			t.Errorf("j = %v, want [a 1 b 2]", j)
		}
	})

	t.Run("float number converts correctly", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(`[1.5, 3]`)
		if err != nil {
			t.Fatalf("Scan(floats) returned error: %v", err)
		}
		if len(j) != 2 {
			t.Fatalf("len = %d, want 2", len(j))
		}
		if j[0] != "1.5" {
			t.Errorf("j[0] = %q, want %q", j[0], "1.5")
		}
		if j[1] != "3" {
			t.Errorf("j[1] = %q, want %q", j[1], "3")
		}
	})

	t.Run("truly malformed JSON still errors", func(t *testing.T) {
		var j JSONArray
		err := j.Scan(`not json at all`)
		if err == nil {
			t.Fatal("expected error for non-JSON input")
		}
	})
}

func TestJSONArrayValue(t *testing.T) {
	t.Run("nil slice", func(t *testing.T) {
		var j JSONArray
		val, err := j.Value()
		if err != nil {
			t.Fatalf("Value() returned error: %v", err)
		}
		if val != nil {
			t.Errorf("expected nil, got %v", val)
		}
	})

	t.Run("valid slice", func(t *testing.T) {
		j := JSONArray{"foo", "bar"}
		val, err := j.Value()
		if err != nil {
			t.Fatalf("Value() returned error: %v", err)
		}
		s, ok := val.(string)
		if !ok {
			t.Fatalf("expected string, got %T", val)
		}
		if s != `["foo","bar"]` {
			t.Errorf("Value() = %q, want %q", s, `["foo","bar"]`)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		j := JSONArray{}
		val, err := j.Value()
		if err != nil {
			t.Fatalf("Value() returned error: %v", err)
		}
		s, ok := val.(string)
		if !ok {
			t.Fatalf("expected string, got %T", val)
		}
		if s != `[]` {
			t.Errorf("Value() = %q, want %q", s, `[]`)
		}
	})
}
