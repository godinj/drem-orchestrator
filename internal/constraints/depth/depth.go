// Package depth provides static depth analysis for Go packages, computing
// export ratios, line counts, and pass-through function detection.
package depth

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DepthReport contains aggregate depth metrics for a single Go package.
type DepthReport struct {
	ExportRatio      float64       // exported symbols / total LOC
	ExportedSymbols  int           // count of exported functions, types, vars, consts
	TotalLOC         int           // total lines of code (non-test .go files)
	PassThroughFuncs []PassThrough // functions that delegate without adding logic
	PackagePath      string        // analyzed package path relative to worktree root
}

// PassThrough describes a function that delegates to another package
// without adding logic.
type PassThrough struct {
	FuncName    string // name of the function
	DelegatePkg string // the package it delegates to
	File        string // source file containing the function
	Line        int    // line number
}

// Analyze analyzes a single Go package directory and returns a depth report.
// pkgPath is relative to worktreeRoot (e.g., "internal/orchestrator").
func Analyze(worktreeRoot, pkgPath string) (*DepthReport, error) {
	dir := filepath.Join(worktreeRoot, pkgPath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading package directory %s: %w", pkgPath, err)
	}

	report := &DepthReport{
		PackagePath: pkgPath,
	}

	fset := token.NewFileSet()

	// Collect import alias -> package name mappings per file for pass-through detection.
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		// Skip test files per PRD user story 17.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		filePath := filepath.Join(dir, name)

		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("reading file %s: %w", filePath, err)
		}

		// Count lines of code.
		loc := countLines(data)
		report.TotalLOC += loc

		// Parse the file for AST analysis.
		f, err := parser.ParseFile(fset, filePath, data, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing file %s: %w", filePath, err)
		}

		// Build import alias map: alias/name -> import path.
		importAliases := buildImportMap(f)

		// Walk AST to count exported symbols and detect pass-throughs.
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if isExported(d.Name.Name) {
					report.ExportedSymbols++
				}
				// Check for pass-through pattern.
				if pt, ok := detectPassThrough(d, importAliases, fset); ok {
					pt.File = name
					report.PassThroughFuncs = append(report.PassThroughFuncs, pt)
				}
			case *ast.GenDecl:
				report.ExportedSymbols += countExportedGenDecl(d)
			}
		}
	}

	// Compute export ratio.
	if report.TotalLOC > 0 {
		report.ExportRatio = float64(report.ExportedSymbols) / float64(report.TotalLOC)
	}

	return report, nil
}

// AnalyzeAll analyzes multiple Go packages and returns a map of depth reports
// keyed by package path.
func AnalyzeAll(worktreeRoot string, pkgPaths []string) (map[string]*DepthReport, error) {
	results := make(map[string]*DepthReport, len(pkgPaths))

	for _, pkgPath := range pkgPaths {
		report, err := Analyze(worktreeRoot, pkgPath)
		if err != nil {
			return nil, fmt.Errorf("analyzing %s: %w", pkgPath, err)
		}
		results[pkgPath] = report
	}

	return results, nil
}

// countLines counts the number of lines in a byte slice.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	// If the file doesn't end with a newline, count the last line.
	if data[len(data)-1] != '\n' {
		count++
	}
	return count
}

// isExported returns true if name starts with an uppercase letter.
func isExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper(rune(name[0]))
}

// countExportedGenDecl counts exported types, vars, and consts in a GenDecl.
func countExportedGenDecl(d *ast.GenDecl) int {
	count := 0
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if isExported(s.Name.Name) {
				count++
			}
		case *ast.ValueSpec:
			for _, name := range s.Names {
				if isExported(name.Name) {
					count++
				}
			}
		}
	}
	return count
}

// buildImportMap creates a mapping from local alias/package name to import path.
func buildImportMap(f *ast.File) map[string]string {
	aliases := make(map[string]string)
	for _, imp := range f.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		var localName string
		if imp.Name != nil {
			localName = imp.Name.Name
		} else {
			// Use the last path element as the package name.
			parts := strings.Split(importPath, "/")
			localName = parts[len(parts)-1]
		}
		aliases[localName] = importPath
	}
	return aliases
}

// detectPassThrough checks if a function is a pass-through: its body is a single
// statement that calls a function from a different package with only the function's
// own parameters (possibly reordered) or literals as arguments.
func detectPassThrough(fn *ast.FuncDecl, imports map[string]string, fset *token.FileSet) (PassThrough, bool) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return PassThrough{}, false
	}

	// Collect parameter names.
	paramNames := collectParamNames(fn)

	stmt := fn.Body.List[0]

	// Check for return statement with a single call expression.
	if ret, ok := stmt.(*ast.ReturnStmt); ok {
		if len(ret.Results) == 1 {
			if call, ok := ret.Results[0].(*ast.CallExpr); ok {
				return checkCallPassThrough(call, paramNames, imports, fn, fset)
			}
		}
		// Also handle bare return of a call that returns multiple values.
		// Pattern: return pkg.Func(args...)
		if len(ret.Results) >= 1 {
			if call, ok := ret.Results[0].(*ast.CallExpr); ok && len(ret.Results) == 1 {
				return checkCallPassThrough(call, paramNames, imports, fn, fset)
			}
		}
	}

	// Check for expression statement with a call (void function delegation).
	if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
		if call, ok := exprStmt.X.(*ast.CallExpr); ok {
			return checkCallPassThrough(call, paramNames, imports, fn, fset)
		}
	}

	return PassThrough{}, false
}

// collectParamNames extracts parameter names from a function declaration.
func collectParamNames(fn *ast.FuncDecl) map[string]bool {
	params := make(map[string]bool)
	if fn.Type.Params == nil {
		return params
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			params[name.Name] = true
		}
	}
	return params
}

// checkCallPassThrough determines if a call expression is a pass-through to
// another package, using only the function's own parameters or literals.
func checkCallPassThrough(call *ast.CallExpr, paramNames map[string]bool, imports map[string]string, fn *ast.FuncDecl, fset *token.FileSet) (PassThrough, bool) {
	// The call must be pkg.Func(args...).
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return PassThrough{}, false
	}

	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return PassThrough{}, false
	}

	pkgAlias := pkgIdent.Name

	// Verify this alias maps to an import.
	importPath, isImport := imports[pkgAlias]
	if !isImport {
		return PassThrough{}, false
	}

	// Check all arguments are params or literals.
	for _, arg := range call.Args {
		if !isParamOrLiteral(arg, paramNames) {
			return PassThrough{}, false
		}
	}

	pos := fset.Position(fn.Pos())
	return PassThrough{
		FuncName:    fn.Name.Name,
		DelegatePkg: importPath,
		Line:        pos.Line,
	}, true
}

// isParamOrLiteral returns true if the expression is either a parameter
// reference or a literal value.
func isParamOrLiteral(expr ast.Expr, paramNames map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return paramNames[e.Name]
	case *ast.BasicLit:
		return true
	default:
		return false
	}
}
