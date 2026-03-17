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
