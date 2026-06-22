package promptassets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
)

const Version = "2026-06-22.1"

type DefaultAsset struct {
	Kind     string
	Name     string
	Language string
	Content  string
}

func DefaultsForLanguage(language string) []DefaultAsset {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "cpp", "c++":
		return cppDefaults()
	default:
		return goDefaults()
	}
}

func SeedDefaults(ctx context.Context, db *gorm.DB, projectID uuid.UUID, language string) error {
	if db == nil || projectID == uuid.Nil {
		return nil
	}
	for _, asset := range DefaultsForLanguage(language) {
		var existing model.ProjectPromptAsset
		err := db.WithContext(ctx).Where(
			"project_id = ? AND kind = ? AND name = ?", projectID, asset.Kind, asset.Name,
		).First(&existing).Error
		if err == nil {
			continue
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return fmt.Errorf("load prompt asset %s/%s: %w", asset.Kind, asset.Name, err)
		}
		contentHash := hash(asset.Content)
		row := model.ProjectPromptAsset{
			ID:          uuid.New(),
			ProjectID:   projectID,
			Kind:        asset.Kind,
			Name:        asset.Name,
			Language:    normalizedLanguage(asset.Language),
			Content:     asset.Content,
			ContentHash: contentHash,
			Version:     Version,
			Active:      true,
		}
		if err := db.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("create prompt asset %s/%s: %w", asset.Kind, asset.Name, err)
		}
	}
	return nil
}

func Load(ctx context.Context, db *gorm.DB, projectID uuid.UUID) (map[string]string, map[string]string, error) {
	assets := map[string]string{}
	versions := map[string]string{}
	if db == nil || projectID == uuid.Nil {
		return assets, versions, nil
	}
	var rows []model.ProjectPromptAsset
	if err := db.WithContext(ctx).Where("project_id = ? AND active = ?", projectID, true).Find(&rows).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "no such table") {
			return assets, versions, nil
		}
		return nil, nil, err
	}
	for _, row := range rows {
		key := Key(row.Kind, row.Name)
		assets[key] = row.Content
		versions[key] = fmt.Sprintf("%s@%s:%s", key, row.Version, row.ContentHash)
	}
	return assets, versions, nil
}

func Key(kind, name string) string {
	return strings.TrimSpace(kind) + "." + strings.TrimSpace(name)
}

func hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func normalizedLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "cpp", "c++":
		return "cpp"
	case "":
		return "go"
	default:
		return strings.ToLower(strings.TrimSpace(language))
	}
}

func goDefaults() []DefaultAsset {
	return []DefaultAsset{
		{Kind: "verification", Name: "strategy", Language: "go", Content: "Run `go vet ./... && go test ./...` in one command. Fix all failures in one pass, then re-run once."},
		{Kind: "coder", Name: "test", Language: "go", Content: goTestInstructions},
		{Kind: "coder", Name: "implementation", Language: "go", Content: goImplementationInstructions},
		{Kind: "coder", Name: "default", Language: "go", Content: goDefaultInstructions},
		{Kind: "direct", Name: "coder", Language: "go", Content: goDirectCoderRules},
		{Kind: "direct", Name: "fixer", Language: "go", Content: goDirectFixerRules},
		{Kind: "planner", Name: "guidance", Language: "go", Content: "When creating subtasks for Go packages, include package paths, public API shapes, and `go test ./...` verification."},
	}
}

func cppDefaults() []DefaultAsset {
	return []DefaultAsset{
		{Kind: "verification", Name: "strategy", Language: "cpp", Content: "This is a C++/CMake project. Do not run Go commands. Prefer `cmake --preset test`, `cmake --build --preset test --target <target>`, and targeted Catch2/CTest commands."},
		{Kind: "coder", Name: "test", Language: "cpp", Content: cppTestInstructions},
		{Kind: "coder", Name: "implementation", Language: "cpp", Content: cppImplementationInstructions},
		{Kind: "coder", Name: "default", Language: "cpp", Content: cppDefaultInstructions},
		{Kind: "direct", Name: "coder", Language: "cpp", Content: cppDirectCoderRules},
		{Kind: "direct", Name: "fixer", Language: "cpp", Content: cppDirectFixerRules},
		{Kind: "planner", Name: "guidance", Language: "cpp", Content: "Plan C++/CMake work with exact files, CMake targets, target visibility, and verification commands. Do not emit Go package rules."},
	}
}

const goTestInstructions = "When writing tests, follow repository Go test infrastructure rules: use shared testutil DB/git helpers, avoid local test factories, and verify with `go vet ./... && go test ./...`."
const goImplementationInstructions = "Implement the minimum Go code to satisfy pre-written tests. Do not modify TDD tests unless they are genuinely wrong. Verify with `go vet ./... && go test ./...`."
const goDefaultInstructions = "Implement the task in Go using existing project patterns. If tests are changed, use shared testutil helpers and verify with `go vet ./... && go test ./...`."
const goDirectCoderRules = "STEP 3: Run `go vet ./... && go test ./...`. Use shared Go test helpers (`internal/testutil`) for DB and git fixtures."
const goDirectFixerRules = "Verify with `go vet ./... && go test ./...` in a single bash command."

const cppTestInstructions = `This is a C++/CMake test task.

- Do not run ` + "`go vet`" + ` or ` + "`go test`" + `.
- Use exact existing test locations and CMake targets from the task; do not invent Go-style test infrastructure.
- Characterization tests should pass on current behavior unless the task explicitly says this is a red-state API or CMake target-contract test.
- Do not create stubs unless the task explicitly asks for a new API contract stub.
- Verify with ` + "`cmake --preset test`" + `, ` + "`cmake --build --preset test --target <target>`" + `, and the narrow Catch2/CTest command named by the task.`

const cppImplementationInstructions = `This is a C++/CMake implementation task.

- Do not run Go commands.
- Read the pre-written tests first and implement the smallest production change needed.
- Do not modify pre-written tests unless they contain a genuine bug.
- Verify with ` + "`cmake --preset test`" + `, ` + "`cmake --build --preset test --target <target>`" + `, and targeted Catch2/CTest commands.`

const cppDefaultInstructions = `This is a C++/CMake coding task.

- Do not run Go commands.
- Use the repository's existing CMake/test layout and exact paths from the task.
- Keep changes focused; avoid broad CMake or source-list rewrites unless the task explicitly requires them.
- Verify with ` + "`cmake --preset test`" + ` and a narrow build/test target.`

const cppDirectCoderRules = `STEP 3: Run the narrow C++/CMake verification command for this task, usually ` + "`cmake --preset test`" + ` followed by ` + "`cmake --build --preset test --target <target>`" + `.

RULES:
- Do not run ` + "`go vet`" + ` or ` + "`go test`" + `.
- Do not invent ` + "`tests/vim`" + ` when the task names ` + "`tests/unit/vim`" + ` or ` + "`tests/integration`" + `.
- Do not create stubs unless the task explicitly requires a red-state API contract.
- If the file already contains the content you intended to write, stop editing and commit or report the blocker.`

const cppDirectFixerRules = "Verify with the narrow C++/CMake command relevant to the diagnosis. Do not run Go commands. Commit the minimal fix."
