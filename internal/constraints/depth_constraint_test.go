package constraints

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalDepth(t *testing.T) {
	tests := []struct {
		name       string
		constraint DepthConstraint
		files      map[string]string // relPath -> content
		fileSet    map[string]bool   // nil for full evaluation
		wantPass   bool
		wantMsg    string // substring expected in messages
	}{
		{
			name: "package within export ratio limit passes",
			constraint: DepthConstraint{
				Name:            "export ratio",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.15,
				MaxPassThroughs: 3,
			},
			files: map[string]string{
				// 2 exported symbols, 30 lines -> ratio 2/30 = 0.0667
				"src/pkg/deep.go": `package pkg

import "fmt"

// Alpha is an exported function.
func Alpha() string {
	a := "hello"
	b := "world"
	c := fmt.Sprintf("%s %s", a, b)
	d := len(c)
	e := d * 2
	f := fmt.Sprintf("%d", e)
	g := strings.ToUpper(f)
	h := g + "!"
	return h
}

// Beta is another exported function.
func Beta() int {
	x := 1
	y := 2
	z := x + y
	w := z * 3
	v := w - 1
	u := v + 10
	q := u / 2
	return q
}

func helper() {}
`,
			},
			wantPass: true,
		},
		{
			name: "package exceeding export ratio fails",
			constraint: DepthConstraint{
				Name:            "export ratio",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.05,
				MaxPassThroughs: 10,
			},
			files: map[string]string{
				// Many exports, few lines -> high ratio
				"src/shallow/api.go": `package shallow

func A() {}
func B() {}
func C() {}
func D() {}
func E() {}
`,
			},
			wantPass: false,
			wantMsg:  "export ratio",
		},
		{
			name: "package with too many pass-throughs fails",
			constraint: DepthConstraint{
				Name:            "pass-throughs",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  1.0, // generous ratio so only pass-throughs matter
				MaxPassThroughs: 1,
			},
			files: map[string]string{
				"src/facade/facade.go": `package facade

import "fmt"

func PrintA(s string) {
	fmt.Println(s)
}

func PrintB(s string) {
	fmt.Println(s)
}
`,
			},
			wantPass: false,
			wantMsg:  "pass-throughs, exceeds limit of 1",
		},
		{
			name: "pass-throughs within limit passes",
			constraint: DepthConstraint{
				Name:            "pass-throughs",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  1.0,
				MaxPassThroughs: 5,
			},
			files: map[string]string{
				"src/facade/facade.go": `package facade

import "fmt"

func PrintA(s string) {
	fmt.Println(s)
}
`,
			},
			wantPass: true,
		},
		{
			name: "grandfathered exception allows existing violations",
			constraint: DepthConstraint{
				Name:            "export ratio with exception",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.01, // very strict
				MaxPassThroughs: 0,    // very strict
				Exceptions: []DepthException{
					{
						Path:              "src/legacy/",
						Rule:              "grandfathered",
						BaselineRatio:     1.0, // generous baseline
						BaselinePassThrus: 100,
					},
				},
			},
			files: map[string]string{
				// This would violate the 0.01 limit but is grandfathered
				"src/legacy/api.go": `package legacy

import "fmt"

func A() { fmt.Println("a") }
func B() { fmt.Println("b") }
func C() { fmt.Println("c") }
`,
			},
			wantPass: true,
		},
		{
			name: "grandfathered exception rejects growth beyond baseline",
			constraint: DepthConstraint{
				Name:            "export ratio with exception",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.01,
				MaxPassThroughs: 0,
				Exceptions: []DepthException{
					{
						Path:              "src/legacy/",
						Rule:              "grandfathered",
						BaselineRatio:     0.10, // tight baseline
						BaselinePassThrus: 100,
					},
				},
			},
			files: map[string]string{
				// High ratio should exceed the baseline
				"src/legacy/api.go": `package legacy

func A() {}
func B() {}
func C() {}
func D() {}
func E() {}
`,
			},
			wantPass: false,
			wantMsg:  "shrink-only baseline",
		},
		{
			name: "test-only package is skipped",
			constraint: DepthConstraint{
				Name:            "skip test packages",
				Glob:            "src/**/*.go",
				MaxExportRatio:  0.01, // very strict
				MaxPassThroughs: 0,
			},
			files: map[string]string{
				// A package with only test files should be skipped entirely
				"src/testpkg/helpers_test.go": `package testpkg

func TestHelper() {}
func TestAnother() {}
func TestMore() {}
`,
			},
			wantPass: true,
		},
		{
			name: "test files excluded from analysis",
			constraint: DepthConstraint{
				Name:            "exclude test files",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.15,
				MaxPassThroughs: 3,
			},
			files: map[string]string{
				// Real source is deep (low ratio)
				"src/pkg/impl.go": `package pkg

import "fmt"

func Run() string {
	a := "hello"
	b := "world"
	c := fmt.Sprintf("%s %s", a, b)
	d := len(c)
	e := d * 2
	f := fmt.Sprintf("%d", e)
	g := f + "!"
	return g
}

func helper1() {}
func helper2() {}
func helper3() {}
`,
				// Test file has many exported symbols that should NOT be counted
				"src/pkg/impl_test.go": `package pkg

func TestExportedA() {}
func TestExportedB() {}
func TestExportedC() {}
func TestExportedD() {}
func TestExportedE() {}
func TestExportedF() {}
func TestExportedG() {}
func TestExportedH() {}
`,
			},
			wantPass: true,
		},
		{
			name: "fileSet restricts evaluation to relevant packages",
			constraint: DepthConstraint{
				Name:            "fileset mode",
				Glob:            "src/**/*.go",
				Exclude:         []string{"*_test.go"},
				MaxExportRatio:  0.01, // very strict — bad package violates this
				MaxPassThroughs: 0,
			},
			files: map[string]string{
				// This package violates the limit but is NOT in fileSet
				"src/bad/api.go": `package bad

func A() {}
func B() {}
func C() {}
`,
				// This package also exists but has no files in fileSet
				"src/other/other.go": `package other

func X() {}
`,
			},
			// No files from any violating package are in fileSet, so
			// those packages are skipped entirely.
			fileSet:  map[string]bool{"src/nonexistent/file.go": true},
			wantPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tt.files {
				writeFile(t, root, path, content)
			}

			result, err := evalDepth(tt.constraint, root, tt.fileSet)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Passed != tt.wantPass {
				t.Errorf("expected passed=%v, got %v; messages: %v", tt.wantPass, result.Passed, result.Messages)
			}
			if tt.wantMsg != "" {
				found := false
				for _, msg := range result.Messages {
					if strings.Contains(msg, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected message containing %q, got %v", tt.wantMsg, result.Messages)
				}
			}
		})
	}
}

func TestEvalDepthFileSetOnlyRelevant(t *testing.T) {
	root := t.TempDir()

	// Create a violating package that is NOT in the fileSet.
	writeFile(t, root, "src/violator/api.go", `package violator

func A() {}
func B() {}
func C() {}
func D() {}
func E() {}
`)

	// Create a conforming package that IS in the fileSet.
	// 1 exported symbol across 30 lines -> ratio = 1/30 = 0.033
	writeFile(t, root, "src/conformer/deep.go", `package conformer

import "fmt"

func Run() string {
	a := "hello"
	b := "world"
	c := fmt.Sprintf("%s %s", a, b)
	d := len(c)
	e := d * 2
	f := fmt.Sprintf("%d", e)
	g := f + "!"
	h := len(g)
	i := h + 1
	j := fmt.Sprintf("%d", i)
	k := j + "end"
	l := len(k)
	m := l + 1
	n := fmt.Sprintf("%d", m)
	o := n + "final"
	p := len(o)
	q := p + 1
	r := fmt.Sprintf("%d", q)
	return r
}

func helper1() {}
func helper2() {}
func helper3() {}
`)

	constraint := DepthConstraint{
		Name:            "fileset scoping",
		Glob:            "src/**/*.go",
		Exclude:         []string{"*_test.go"},
		MaxExportRatio:  0.05,
		MaxPassThroughs: 0,
	}

	// Only evaluate the conforming package.
	fileSet := map[string]bool{
		filepath.Join("src", "conformer", "deep.go"): true,
	}

	result, err := evalDepth(constraint, root, fileSet)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected pass (violating package not in fileSet), got fail; messages: %v", result.Messages)
	}
}

func TestFindDepthException(t *testing.T) {
	exceptions := []DepthException{
		{Path: "internal/tui/", Rule: "grandfathered", BaselineRatio: 1.0, BaselinePassThrus: 100},
		{Path: "internal/legacy", Rule: "grandfathered", BaselineRatio: 0.5, BaselinePassThrus: 10},
	}

	tests := []struct {
		name  string
		dir   string
		want  bool
		wantR float64
	}{
		{"trailing slash match", "internal/tui", true, 1.0},
		{"exact match without slash", "internal/legacy", true, 0.5},
		{"no match", "internal/other", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc, found := findDepthException(exceptions, tt.dir)
			if found != tt.want {
				t.Errorf("expected found=%v, got %v", tt.want, found)
			}
			if found && exc.BaselineRatio != tt.wantR {
				t.Errorf("expected baseline ratio %v, got %v", tt.wantR, exc.BaselineRatio)
			}
		})
	}
}

func TestLoadConfigWithDepth(t *testing.T) {
	dir := t.TempDir()

	tomlContent := `
[[depth]]
name              = "Export ratio ceiling"
glob              = "internal/**/*.go"
exclude           = ["*_test.go"]
max_export_ratio  = 0.15
max_pass_throughs = 3

[[depth.exception]]
path               = "internal/tui/"
rule               = "grandfathered"
baseline_ratio     = 1.0
baseline_pass_thrus = 100
`
	writeFile(t, dir, ".drem/constraints.toml", tomlContent)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Depth) != 1 {
		t.Fatalf("expected 1 depth constraint, got %d", len(cfg.Depth))
	}

	dc := cfg.Depth[0]
	if dc.Name != "Export ratio ceiling" {
		t.Errorf("unexpected name: %s", dc.Name)
	}
	if dc.Glob != "internal/**/*.go" {
		t.Errorf("unexpected glob: %s", dc.Glob)
	}
	if len(dc.Exclude) != 1 || dc.Exclude[0] != "*_test.go" {
		t.Errorf("unexpected exclude: %v", dc.Exclude)
	}
	if dc.MaxExportRatio != 0.15 {
		t.Errorf("unexpected max_export_ratio: %v", dc.MaxExportRatio)
	}
	if dc.MaxPassThroughs != 3 {
		t.Errorf("unexpected max_pass_throughs: %d", dc.MaxPassThroughs)
	}
	if len(dc.Exceptions) != 1 {
		t.Fatalf("expected 1 exception, got %d", len(dc.Exceptions))
	}

	exc := dc.Exceptions[0]
	if exc.Path != "internal/tui/" {
		t.Errorf("unexpected exception path: %s", exc.Path)
	}
	if exc.Rule != "grandfathered" {
		t.Errorf("unexpected exception rule: %s", exc.Rule)
	}
	if exc.BaselineRatio != 1.0 {
		t.Errorf("unexpected baseline_ratio: %v", exc.BaselineRatio)
	}
	if exc.BaselinePassThrus != 100 {
		t.Errorf("unexpected baseline_pass_thrus: %d", exc.BaselinePassThrus)
	}
}
