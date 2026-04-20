package projects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// FileKind selects the parsing strategy for Diff.
type FileKind int

const (
	// FileKindCompose parses with gopkg.in/yaml.v3. Comments and
	// whitespace are discarded.
	FileKindCompose FileKind = iota
	// FileKindDremToml parses with github.com/BurntSushi/toml.
	// Comments and whitespace are discarded.
	FileKindDremToml
)

// DriftEntry is a single structural difference between the rendered
// template output and the on-disk file. Returned sorted by Path for
// deterministic operator output.
type DriftEntry struct {
	// Path is a dotted path identifying the scalar location. For
	// compose files the shape is services.<name>.environment.<KEY> or
	// services.<name>.ports[<i>] etc. For drem.toml it is the TOML
	// key path (agents.planner.provider).
	Path string

	// Kind is "added" (present in rendered, absent on disk),
	// "removed" (present on disk, absent in rendered), or "changed"
	// (different scalar value).
	Kind string

	// WasValue is the on-disk scalar (empty when Kind=="added").
	WasValue string

	// NewValue is the rendered scalar (empty when Kind=="removed").
	NewValue string
}

// Diff compares rendered against onDisk and returns drift entries. A
// nil return signals "no drift." Whitespace and comment differences
// are intentionally not drift; Diff normalizes by parsing both sides
// into structured form and walking the tree. A parse failure on
// either side produces zero entries — Diff is advisory; authoritative
// read-parse errors are the caller's to handle upstream
// (ReadStateFromDisk).
func Diff(rendered, onDisk []byte, kind FileKind) []DriftEntry {
	switch kind {
	case FileKindCompose:
		return diffYAML(rendered, onDisk)
	case FileKindDremToml:
		return diffTOML(rendered, onDisk)
	}
	return nil
}

// diffYAML parses both inputs as YAML (as map[string]any trees) and
// recursively walks to find scalar-level drift. Returns entries sorted
// by Path.
func diffYAML(rendered, onDisk []byte) []DriftEntry {
	var r, d map[string]any
	if err := yaml.Unmarshal(rendered, &r); err != nil {
		return []DriftEntry{}
	}
	if err := yaml.Unmarshal(onDisk, &d); err != nil {
		return []DriftEntry{}
	}
	entries := walkDiff("", r, d)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// diffTOML parses both inputs as TOML and reuses walkDiff. The
// BurntSushi/toml unmarshaller into map[string]any produces nested
// maps + scalars, matching yaml.Unmarshal's shape closely enough that
// walkDiff handles both.
func diffTOML(rendered, onDisk []byte) []DriftEntry {
	var r, d map[string]any
	if err := toml.Unmarshal(rendered, &r); err != nil {
		return []DriftEntry{}
	}
	if err := toml.Unmarshal(onDisk, &d); err != nil {
		return []DriftEntry{}
	}
	entries := walkDiff("", r, d)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// walkDiff recursively compares two tree nodes and emits drift entries
// for every scalar-level difference. prefix is the dotted path
// accumulated from the root.
func walkDiff(prefix string, rendered, onDisk any) []DriftEntry {
	var entries []DriftEntry
	rMap, rOk := asMap(rendered)
	dMap, dOk := asMap(onDisk)
	if rOk && dOk {
		keys := unionKeys(rMap, dMap)
		for _, k := range keys {
			p := joinPath(prefix, k)
			rv, rHas := rMap[k]
			dv, dHas := dMap[k]
			switch {
			case rHas && !dHas:
				entries = append(entries, scalarOrDeep(p, rv, nil, "added")...)
			case !rHas && dHas:
				entries = append(entries, scalarOrDeep(p, nil, dv, "removed")...)
			default:
				entries = append(entries, walkDiff(p, rv, dv)...)
			}
		}
		return entries
	}
	// Both scalar (or one/both are lists — see handling below).
	if rList, dList, bothLists := asBothLists(rendered, onDisk); bothLists {
		return diffLists(prefix, rList, dList)
	}
	rs := scalar(rendered)
	ds := scalar(onDisk)
	if rs != ds {
		entries = append(entries, DriftEntry{
			Path:     prefix,
			Kind:     "changed",
			WasValue: ds,
			NewValue: rs,
		})
	}
	return entries
}

// scalarOrDeep emits "added" or "removed" entries for a subtree. For
// scalar leaves it's a single entry; for maps it flattens every leaf
// path so the operator sees which keys under a newly-added section
// are landing. Exactly one of rendered/onDisk is non-nil — the side
// matching kind.
func scalarOrDeep(prefix string, rendered, onDisk any, kind string) []DriftEntry {
	subtree := rendered
	if kind == "removed" {
		subtree = onDisk
	}
	if m, ok := asMap(subtree); ok {
		var entries []DriftEntry
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := joinPath(prefix, k)
			child := m[k]
			if kind == "added" {
				entries = append(entries, scalarOrDeep(p, child, nil, kind)...)
			} else {
				entries = append(entries, scalarOrDeep(p, nil, child, kind)...)
			}
		}
		return entries
	}
	// Leaf.
	e := DriftEntry{Path: prefix, Kind: kind}
	if kind == "added" {
		e.NewValue = scalar(rendered)
	} else {
		e.WasValue = scalar(onDisk)
	}
	return []DriftEntry{e}
}

// asMap returns the value as a map[string]any if possible. Handles
// both map[string]any (yaml.v3 default) and the rarer map[any]any
// shape. TOML unmarshal into any yields map[string]any so no special
// handling there.
func asMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if m, ok := v.(map[any]any); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

// asBothLists reports whether both values are []any and returns them.
func asBothLists(a, b any) ([]any, []any, bool) {
	al, aOk := a.([]any)
	bl, bOk := b.([]any)
	return al, bl, aOk && bOk
}

// diffLists compares two []any. Strategy: positional comparison. For
// compose ports/volumes this is adequate — the template emits them in
// a fixed order and the on-disk copy normally preserves it. Mismatches
// at a position generate a "changed" entry at Path[i]; extra elements
// on either side come out as added/removed.
func diffLists(prefix string, rendered, onDisk []any) []DriftEntry {
	var entries []DriftEntry
	max := len(rendered)
	if len(onDisk) > max {
		max = len(onDisk)
	}
	for i := 0; i < max; i++ {
		p := fmt.Sprintf("%s[%d]", prefix, i)
		switch {
		case i >= len(onDisk):
			entries = append(entries, DriftEntry{Path: p, Kind: "added", NewValue: scalar(rendered[i])})
		case i >= len(rendered):
			entries = append(entries, DriftEntry{Path: p, Kind: "removed", WasValue: scalar(onDisk[i])})
		default:
			rs := scalar(rendered[i])
			ds := scalar(onDisk[i])
			if rs != ds {
				entries = append(entries, DriftEntry{
					Path:     p,
					Kind:     "changed",
					WasValue: ds,
					NewValue: rs,
				})
			}
		}
	}
	return entries
}

// unionKeys returns the sorted union of keys across two maps.
func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joinPath joins a prefix and key with a dot, respecting empty prefix.
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// scalar returns the stringified form of a leaf value. Used for both
// comparison and for populating DriftEntry.WasValue / NewValue.
func scalar(v any) string {
	if v == nil {
		return ""
	}
	// Compose YAML numbers unmarshal as int/float; string "8080" and
	// int 8080 must compare equal. Normalize via fmt.Sprint.
	s := fmt.Sprint(v)
	// Trim surrounding quotes if any sneaked in.
	return strings.TrimSpace(s)
}
