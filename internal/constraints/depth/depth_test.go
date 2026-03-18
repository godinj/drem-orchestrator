package depth

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeFixture creates a Go source file in the given directory.
func writeFixture(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing fixture %s: %v", filename, err)
	}
}

// setupPkgDir creates a package subdirectory under the temp root.
func setupPkgDir(t *testing.T, root, pkgPath string) string {
	t.Helper()
	dir := filepath.Join(root, pkgPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("creating package dir %s: %v", pkgPath, err)
	}
	return dir
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name             string
		pkgPath          string
		files            map[string]string
		wantExported     int
		wantLOCMin       int // minimum expected LOC (exact depends on formatting)
		wantLOCMax       int // maximum expected LOC
		wantPassThroughs int
		wantRatioLow     bool // true if ratio should be low (deep module)
		wantRatioHigh    bool // true if ratio should be high (shallow module)
	}{
		{
			name:    "deep module - many LOC, few exports",
			pkgPath: "deep",
			files: map[string]string{
				"deep.go": `package deep

import "fmt"

// internalHelper does heavy lifting.
func internalHelper(x int) int {
	result := x * 2
	if result > 100 {
		result = 100
	}
	if result < 0 {
		result = 0
	}
	for i := 0; i < result; i++ {
		result += i
	}
	return result
}

func anotherHelper(a, b int) int {
	sum := a + b
	diff := a - b
	product := a * b
	if product > 1000 {
		product = 1000
	}
	if sum > diff {
		return sum + product
	}
	return diff + product
}

func yetAnother(s string) string {
	result := ""
	for _, c := range s {
		result += string(c)
		result += "-"
	}
	if len(result) > 0 {
		result = result[:len(result)-1]
	}
	return result
}

func moreInternal(x, y, z int) int {
	a := x + y
	b := y + z
	c := x + z
	if a > b && a > c {
		return a
	}
	if b > c {
		return b
	}
	return c
}

func evenMore(vals []int) int {
	total := 0
	for _, v := range vals {
		total += v
	}
	if total == 0 {
		return -1
	}
	return total
}

// DoWork is the only exported function.
func DoWork(x int) int {
	r := internalHelper(x)
	r = anotherHelper(r, x)
	return r
}

// RunProcess is another exported function.
func RunProcess(s string) string {
	return yetAnother(s)
}

// Value is an exported constant.
const Value = 42

var _ = fmt.Sprintf
`,
			},
			wantExported:  3, // DoWork, RunProcess, Value
			wantLOCMin:    70,
			wantLOCMax:    90,
			wantRatioLow:  true,
			wantRatioHigh: false,
		},
		{
			name:    "shallow module - few LOC, many exports",
			pkgPath: "shallow",
			files: map[string]string{
				"shallow.go": `package shallow

// Alpha is exported.
func Alpha() {}

// Beta is exported.
func Beta() {}

// Gamma is exported.
func Gamma() {}

// Delta is exported.
func Delta() {}

// Epsilon is exported.
func Epsilon() {}

// Zeta is exported.
func Zeta() {}

// Eta is exported.
func Eta() {}

// Theta is exported.
func Theta() {}

// Iota is exported.
func Iota() {}

// Kappa is exported.
func Kappa() {}

// Lambda is exported.
func Lambda() {}

// Mu is exported.
func Mu() {}

// TypeA is an exported type.
type TypeA struct{}

// TypeB is an exported type.
type TypeB struct{}

// ConstA is an exported const.
const ConstA = 1
`,
			},
			wantExported:  15, // 12 funcs + 2 types + 1 const
			wantLOCMin:    30,
			wantLOCMax:    50,
			wantRatioHigh: true,
			wantRatioLow:  false,
		},
		{
			name:    "pass-through functions",
			pkgPath: "passthrough",
			files: map[string]string{
				"forward.go": `package passthrough

import "other/pkg"

// ForwardCall delegates to pkg.DoThing.
func ForwardCall(a int, b string) error {
	return pkg.DoThing(a, b)
}

// ForwardVoid delegates to pkg.Execute without return.
func ForwardVoid(x int) {
	pkg.Execute(x)
}

// RealLogic is not a pass-through.
func RealLogic(a int) int {
	result := a * 2
	if result > 100 {
		result = 100
	}
	return result
}
`,
			},
			wantExported:     3, // ForwardCall, ForwardVoid, RealLogic
			wantLOCMin:       15,
			wantLOCMax:       30,
			wantPassThroughs: 2, // ForwardCall, ForwardVoid
		},
		{
			name:    "mixed - some pass-throughs and real logic",
			pkgPath: "mixed",
			files: map[string]string{
				"api.go": `package mixed

import "backend/service"

// Create delegates to service.
func Create(name string) error {
	return service.Create(name)
}

// Delete delegates to service.
func Delete(id int) error {
	return service.Delete(id)
}

// Process does real work.
func Process(data []byte) ([]byte, error) {
	result := make([]byte, len(data))
	for i, b := range data {
		result[i] = b ^ 0xFF
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

type config struct {
	name string
	val  int
}

func newConfig(n string, v int) config {
	return config{name: n, val: v}
}
`,
			},
			wantExported:     3, // Create, Delete, Process
			wantLOCMin:       20,
			wantLOCMax:       40,
			wantPassThroughs: 2, // Create, Delete
		},
		{
			name:    "test file exclusion",
			pkgPath: "testexclude",
			files: map[string]string{
				"main.go": `package testexclude

// Do is exported.
func Do() {}

// Run is exported.
func Run() {}
`,
				"main_test.go": `package testexclude

// TestHelper is an exported symbol in a test file.
func TestHelper() {}

// BenchHelper is exported too.
func BenchHelper() {}

// ExportedTestType should be excluded.
type ExportedTestType struct{}
`,
			},
			wantExported:     2, // only Do, Run from main.go
			wantLOCMin:       5,
			wantLOCMax:       10,
			wantPassThroughs: 0,
		},
		{
			name:    "multiple files in package",
			pkgPath: "multi",
			files: map[string]string{
				"a.go": `package multi

// FuncA is exported.
func FuncA() int {
	return 1
}

var internalA = 10
`,
				"b.go": `package multi

// FuncB is exported.
func FuncB() int {
	return 2
}

// TypeB is exported.
type TypeB struct {
	Value int
}
`,
			},
			wantExported:     3, // FuncA, FuncB, TypeB
			wantLOCMin:       10,
			wantLOCMax:       25,
			wantPassThroughs: 0,
		},
		{
			name:    "pass-through with reordered args",
			pkgPath: "reorder",
			files: map[string]string{
				"reorder.go": `package reorder

import "other/service"

// Forward reorders parameters before delegating.
func Forward(a int, b string) error {
	return service.Call(b, a)
}
`,
			},
			wantExported:     1,
			wantLOCMin:       5,
			wantLOCMax:       15,
			wantPassThroughs: 1,
		},
		{
			name:    "pass-through with literal args",
			pkgPath: "literal",
			files: map[string]string{
				"literal.go": `package literal

import "other/service"

// Forward passes a literal alongside params.
func Forward(a int) error {
	return service.Call(a, "default")
}
`,
			},
			wantExported:     1,
			wantLOCMin:       5,
			wantLOCMax:       15,
			wantPassThroughs: 1,
		},
		{
			name:    "not pass-through - computation in args",
			pkgPath: "notpt",
			files: map[string]string{
				"notpt.go": `package notpt

import "other/service"

// Transform is NOT a pass-through because it computes a+1.
func Transform(a int) error {
	return service.Call(a + 1)
}
`,
			},
			wantExported:     1,
			wantLOCMin:       5,
			wantLOCMax:       15,
			wantPassThroughs: 0, // a+1 is computation, not a simple param
		},
		{
			name:    "exported vars and consts",
			pkgPath: "exports",
			files: map[string]string{
				"exports.go": `package exports

// MaxSize is an exported constant.
const MaxSize = 1024

// MinSize is an exported constant.
const MinSize = 0

// DefaultName is an exported variable.
var DefaultName = "test"

// internal is unexported.
var internal = true

// Config is an exported type.
type Config struct{}

// option is unexported.
type option struct{}
`,
			},
			wantExported:     4, // MaxSize, MinSize, DefaultName, Config
			wantLOCMin:       10,
			wantLOCMax:       25,
			wantPassThroughs: 0,
		},
		{
			name:    "grouped const and var declarations",
			pkgPath: "grouped",
			files: map[string]string{
				"grouped.go": `package grouped

const (
	// Alpha is exported.
	Alpha = 1
	// Beta is exported.
	Beta = 2
	// gamma is unexported.
	gamma = 3
)

var (
	// Delta is exported.
	Delta = "d"
	// epsilon is unexported.
	epsilon = "e"
)
`,
			},
			wantExported:     3, // Alpha, Beta, Delta
			wantLOCMin:       10,
			wantLOCMax:       25,
			wantPassThroughs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			pkgDir := setupPkgDir(t, root, tt.pkgPath)

			for name, content := range tt.files {
				writeFixture(t, pkgDir, name, content)
			}

			report, err := Analyze(root, tt.pkgPath)
			if err != nil {
				t.Fatalf("Analyze() error: %v", err)
			}

			if report.PackagePath != tt.pkgPath {
				t.Errorf("PackagePath = %q, want %q", report.PackagePath, tt.pkgPath)
			}

			if report.ExportedSymbols != tt.wantExported {
				t.Errorf("ExportedSymbols = %d, want %d", report.ExportedSymbols, tt.wantExported)
			}

			if report.TotalLOC < tt.wantLOCMin || report.TotalLOC > tt.wantLOCMax {
				t.Errorf("TotalLOC = %d, want between %d and %d", report.TotalLOC, tt.wantLOCMin, tt.wantLOCMax)
			}

			if len(report.PassThroughFuncs) != tt.wantPassThroughs {
				t.Errorf("PassThroughFuncs count = %d, want %d", len(report.PassThroughFuncs), tt.wantPassThroughs)
				for _, pt := range report.PassThroughFuncs {
					t.Logf("  pass-through: %s -> %s (file: %s, line: %d)", pt.FuncName, pt.DelegatePkg, pt.File, pt.Line)
				}
			}

			if tt.wantRatioLow && report.ExportRatio >= 0.1 {
				t.Errorf("ExportRatio = %f, expected low ratio (< 0.1) for deep module", report.ExportRatio)
			}
			if tt.wantRatioHigh && report.ExportRatio < 0.1 {
				t.Errorf("ExportRatio = %f, expected high ratio (>= 0.1) for shallow module", report.ExportRatio)
			}

			// Verify export ratio computation.
			if report.TotalLOC > 0 {
				expectedRatio := float64(report.ExportedSymbols) / float64(report.TotalLOC)
				if math.Abs(report.ExportRatio-expectedRatio) > 0.0001 {
					t.Errorf("ExportRatio = %f, expected %f", report.ExportRatio, expectedRatio)
				}
			}
		})
	}
}

func TestAnalyze_PassThroughDetails(t *testing.T) {
	root := t.TempDir()
	pkgDir := setupPkgDir(t, root, "ptdetail")

	writeFixture(t, pkgDir, "forward.go", `package ptdetail

import "other/pkg"

// ForwardCall delegates to pkg.DoThing.
func ForwardCall(a int, b string) error {
	return pkg.DoThing(a, b)
}
`)

	report, err := Analyze(root, "ptdetail")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	if len(report.PassThroughFuncs) != 1 {
		t.Fatalf("expected 1 pass-through, got %d", len(report.PassThroughFuncs))
	}

	pt := report.PassThroughFuncs[0]
	if pt.FuncName != "ForwardCall" {
		t.Errorf("FuncName = %q, want %q", pt.FuncName, "ForwardCall")
	}
	if pt.DelegatePkg != "other/pkg" {
		t.Errorf("DelegatePkg = %q, want %q", pt.DelegatePkg, "other/pkg")
	}
	if pt.File != "forward.go" {
		t.Errorf("File = %q, want %q", pt.File, "forward.go")
	}
	if pt.Line == 0 {
		t.Error("Line should not be 0")
	}
}

func TestAnalyze_EmptyPackage(t *testing.T) {
	root := t.TempDir()
	setupPkgDir(t, root, "empty")

	report, err := Analyze(root, "empty")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	if report.ExportedSymbols != 0 {
		t.Errorf("ExportedSymbols = %d, want 0", report.ExportedSymbols)
	}
	if report.TotalLOC != 0 {
		t.Errorf("TotalLOC = %d, want 0", report.TotalLOC)
	}
	if report.ExportRatio != 0 {
		t.Errorf("ExportRatio = %f, want 0", report.ExportRatio)
	}
}

func TestAnalyze_NonExistentDirectory(t *testing.T) {
	root := t.TempDir()

	_, err := Analyze(root, "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestAnalyzeAll(t *testing.T) {
	root := t.TempDir()

	// Create two packages.
	pkgDir1 := setupPkgDir(t, root, "pkg1")
	writeFixture(t, pkgDir1, "pkg1.go", `package pkg1

// FuncA is exported.
func FuncA() {}

// FuncB is exported.
func FuncB() {}
`)

	pkgDir2 := setupPkgDir(t, root, "pkg2")
	writeFixture(t, pkgDir2, "pkg2.go", `package pkg2

// FuncX is exported.
func FuncX() {}
`)

	results, err := AnalyzeAll(root, []string{"pkg1", "pkg2"})
	if err != nil {
		t.Fatalf("AnalyzeAll() error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results["pkg1"] == nil {
		t.Fatal("missing result for pkg1")
	}
	if results["pkg1"].ExportedSymbols != 2 {
		t.Errorf("pkg1 ExportedSymbols = %d, want 2", results["pkg1"].ExportedSymbols)
	}

	if results["pkg2"] == nil {
		t.Fatal("missing result for pkg2")
	}
	if results["pkg2"].ExportedSymbols != 1 {
		t.Errorf("pkg2 ExportedSymbols = %d, want 1", results["pkg2"].ExportedSymbols)
	}
}

func TestAnalyzeAll_ErrorPropagation(t *testing.T) {
	root := t.TempDir()

	// Create one valid package.
	pkgDir := setupPkgDir(t, root, "valid")
	writeFixture(t, pkgDir, "valid.go", `package valid

func Do() {}
`)

	// Try to analyze one valid and one non-existent.
	_, err := AnalyzeAll(root, []string{"valid", "nonexistent"})
	if err == nil {
		t.Fatal("expected error when analyzing non-existent package")
	}
}

func TestCompareGrowth(t *testing.T) {
	tests := []struct {
		name                 string
		old                  *DepthReport
		new                  *DepthReport
		wantDisproportionate bool
		wantExportGrowth     float64
		wantLOCGrowth        float64
	}{
		{
			name: "proportional growth",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 10,
				TotalLOC:        100,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 12,
				TotalLOC:        120,
			},
			wantDisproportionate: false,
			wantExportGrowth:     20,
			wantLOCGrowth:        20,
		},
		{
			name: "disproportionate - exports grow faster",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 10,
				TotalLOC:        100,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 20,
				TotalLOC:        110,
			},
			wantDisproportionate: true,
			wantExportGrowth:     100,
			wantLOCGrowth:        10,
		},
		{
			name: "LOC grows faster - not disproportionate",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 10,
				TotalLOC:        100,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 11,
				TotalLOC:        200,
			},
			wantDisproportionate: false,
			wantExportGrowth:     10,
			wantLOCGrowth:        100,
		},
		{
			name: "no change",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 5,
				TotalLOC:        50,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 5,
				TotalLOC:        50,
			},
			wantDisproportionate: false,
			wantExportGrowth:     0,
			wantLOCGrowth:        0,
		},
		{
			name: "shrinking - exports decrease",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 10,
				TotalLOC:        100,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 8,
				TotalLOC:        90,
			},
			wantDisproportionate: false,
			wantExportGrowth:     -20,
			wantLOCGrowth:        -10,
		},
		{
			name: "from zero exports",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 0,
				TotalLOC:        50,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 5,
				TotalLOC:        60,
			},
			wantDisproportionate: true,
			wantExportGrowth:     100,
			wantLOCGrowth:        20,
		},
		{
			name: "from zero LOC",
			old: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 0,
				TotalLOC:        0,
			},
			new: &DepthReport{
				PackagePath:     "pkg",
				ExportedSymbols: 5,
				TotalLOC:        50,
			},
			wantDisproportionate: false, // both are 100%
			wantExportGrowth:     100,
			wantLOCGrowth:        100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gr := CompareGrowth(tt.old, tt.new)

			if gr.PackagePath != tt.new.PackagePath {
				t.Errorf("PackagePath = %q, want %q", gr.PackagePath, tt.new.PackagePath)
			}
			if gr.OldExportedSymbols != tt.old.ExportedSymbols {
				t.Errorf("OldExportedSymbols = %d, want %d", gr.OldExportedSymbols, tt.old.ExportedSymbols)
			}
			if gr.NewExportedSymbols != tt.new.ExportedSymbols {
				t.Errorf("NewExportedSymbols = %d, want %d", gr.NewExportedSymbols, tt.new.ExportedSymbols)
			}
			if gr.OldTotalLOC != tt.old.TotalLOC {
				t.Errorf("OldTotalLOC = %d, want %d", gr.OldTotalLOC, tt.old.TotalLOC)
			}
			if gr.NewTotalLOC != tt.new.TotalLOC {
				t.Errorf("NewTotalLOC = %d, want %d", gr.NewTotalLOC, tt.new.TotalLOC)
			}
			if gr.Disproportionate != tt.wantDisproportionate {
				t.Errorf("Disproportionate = %v, want %v (export growth: %.1f%%, LOC growth: %.1f%%)",
					gr.Disproportionate, tt.wantDisproportionate, gr.ExportGrowthRate, gr.LOCGrowthRate)
			}
			if math.Abs(gr.ExportGrowthRate-tt.wantExportGrowth) > 0.1 {
				t.Errorf("ExportGrowthRate = %.1f, want %.1f", gr.ExportGrowthRate, tt.wantExportGrowth)
			}
			if math.Abs(gr.LOCGrowthRate-tt.wantLOCGrowth) > 0.1 {
				t.Errorf("LOCGrowthRate = %.1f, want %.1f", gr.LOCGrowthRate, tt.wantLOCGrowth)
			}
		})
	}
}

func TestAnalyze_ImportAliasPassThrough(t *testing.T) {
	root := t.TempDir()
	pkgDir := setupPkgDir(t, root, "aliased")

	writeFixture(t, pkgDir, "aliased.go", `package aliased

import svc "backend/service"

// Forward uses an aliased import.
func Forward(x int) error {
	return svc.Run(x)
}
`)

	report, err := Analyze(root, "aliased")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	if len(report.PassThroughFuncs) != 1 {
		t.Fatalf("expected 1 pass-through, got %d", len(report.PassThroughFuncs))
	}

	pt := report.PassThroughFuncs[0]
	if pt.DelegatePkg != "backend/service" {
		t.Errorf("DelegatePkg = %q, want %q", pt.DelegatePkg, "backend/service")
	}
}

func TestAnalyze_MultiStatementBody(t *testing.T) {
	root := t.TempDir()
	pkgDir := setupPkgDir(t, root, "multi")

	writeFixture(t, pkgDir, "multi.go", `package multi

import "other/pkg"

// NotPassThrough has multiple statements.
func NotPassThrough(a int) error {
	a = a + 1
	return pkg.Do(a)
}
`)

	report, err := Analyze(root, "multi")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	if len(report.PassThroughFuncs) != 0 {
		t.Errorf("expected 0 pass-throughs for multi-statement body, got %d", len(report.PassThroughFuncs))
	}
}

func TestAnalyze_MethodNotPassThrough(t *testing.T) {
	// Methods calling same-package receiver methods should not be pass-throughs.
	root := t.TempDir()
	pkgDir := setupPkgDir(t, root, "methods")

	writeFixture(t, pkgDir, "methods.go", `package methods

import "other/pkg"

type Service struct{}

// Forward on a receiver still counts as pass-through to external pkg.
func (s Service) Forward(x int) error {
	return pkg.Do(x)
}
`)

	report, err := Analyze(root, "methods")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// Methods with receiver can still be pass-throughs if they delegate to external pkg.
	if len(report.PassThroughFuncs) != 1 {
		t.Errorf("expected 1 pass-through, got %d", len(report.PassThroughFuncs))
	}
}

func TestAnalyze_NonGoFilesIgnored(t *testing.T) {
	root := t.TempDir()
	pkgDir := setupPkgDir(t, root, "nongo")

	writeFixture(t, pkgDir, "code.go", `package nongo

// Do is exported.
func Do() {}
`)
	// Write a non-Go file that should be ignored.
	writeFixture(t, pkgDir, "readme.txt", `This is not Go code and should be ignored.
It has multiple lines.
`)

	report, err := Analyze(root, "nongo")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	if report.ExportedSymbols != 1 {
		t.Errorf("ExportedSymbols = %d, want 1 (non-Go files should be ignored)", report.ExportedSymbols)
	}
}
