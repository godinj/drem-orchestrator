// drembench-gen-real generates drembench task spec JSON files from real tasks
// in the drem database, using prompt.GenerateDirectCoder to produce an
// apples-to-apples system prompt against the prod dispatch path.
//
// Output specs point the fixture at bench/fixtures/real-repo (a snapshot of
// the current repo source). Verifiers are pragmatic: they require the task's
// declared test symbols to exist AND `go vet ./...` to pass. They do NOT
// require the tests themselves to pass, because several of these tasks are
// TDD-style and explicitly expect the implementation to follow later.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/internal/prompt"
)

type spec struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	FixtureDir   string `json:"fixture_dir"`
	Role         string `json:"role"`
	SystemPrompt string `json:"system_prompt"`
	UserMessage  string `json:"user_message"`
	VerifierCmd  string `json:"verifier_cmd"`
}

// taskDef couples a DB task id with the verifier test symbols to look for.
type taskDef struct {
	id       string
	alias    string
	symbols  []string // each must appear in some *.go file under the fixture
	pkgPaths []string // go vet target paths (optional narrowing)
}

func main() {
	var (
		dbPath = flag.String("db", "drem.db", "path to drem.db")
		outDir = flag.String("out", "bench/tasks", "output directory for JSON specs")
	)
	flag.Parse()

	defs := []taskDef{
		{
			id:       "44aad18c-a929-4449-a6da-98197f3e7c7a",
			alias:    "real-constraint-gate-exhaustion",
			symbols:  []string{"TestConstraintGateWiring_ExhaustionState_MaxRetries", "TestConstraintGateWiring_ExhaustionState_EarlyTermination", "TestConstraintGateWiring_ExhaustionState_ClearedOnPass"},
			pkgPaths: []string{"./internal/orchestrator/..."},
		},
		{
			id:       "a46c8a83-a22d-4f1b-8076-5acf446d66fd",
			alias:    "real-retry-suppression",
			symbols:  []string{"TestReconcileFailedParents_ConstraintGateExhausted_NoNewCommits", "TestReconcileFailedParents_ConstraintGateExhausted_NewCommit", "TestReconcileFailedParents_NonConstraintFailure_StillRecovers"},
			pkgPaths: []string{"./internal/orchestrator/..."},
		},
		{
			id:       "b0d90304-074a-4edb-ad2b-242b34ffcbe3",
			alias:    "real-csuite-inbox-cli",
			symbols:  []string{"TestSendCommand_CreatesMessage", "TestListCommand_ShowsUnreadMessages", "TestArchiveCommand_ArchivesMessage"},
			pkgPaths: []string{"./..."},
		},
	}

	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	for _, d := range defs {
		var title, description sql.NullString
		err := db.QueryRow(`SELECT title, description FROM tasks WHERE id=?`, d.id).Scan(&title, &description)
		if err != nil {
			log.Fatalf("query task %s: %v", d.id, err)
		}
		parsedID, err := uuid.Parse(d.id)
		if err != nil {
			log.Fatalf("parse id %s: %v", d.id, err)
		}
		tk := &model.Task{
			ID:          parsedID,
			Title:       title.String,
			Description: description.String,
		}
		sysPrompt := prompt.GenerateDirectCoder(prompt.Opts{
			Task:         tk,
			WorktreePath: ".", // drembench sets WorkDir at runtime; model sees CWD as "."
		})
		userMsg := tk.Description
		if userMsg == "" {
			userMsg = tk.Title
		}

		verifier := buildVerifier(d.symbols, d.pkgPaths)

		s := spec{
			Name:         d.alias,
			Description:  title.String,
			FixtureDir:   "bench/fixtures/real-repo",
			Role:         "coder",
			SystemPrompt: sysPrompt,
			UserMessage:  userMsg,
			VerifierCmd:  verifier,
		}
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			log.Fatalf("marshal %s: %v", d.alias, err)
		}
		path := filepath.Join(*outDir, d.alias+".json")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d bytes prompt)\n", path, len(sysPrompt))
	}
}

// buildVerifier produces a bash command that succeeds if and only if every
// symbol appears in at least one *.go file under CWD, AND `go vet` passes.
// Test pass is NOT required — these are TDD scaffolding tasks.
func buildVerifier(symbols, pkgs []string) string {
	var parts []string
	for _, sym := range symbols {
		parts = append(parts, fmt.Sprintf("grep -rq -- %s --include='*.go' .", shellQuote(sym)))
	}
	for _, p := range pkgs {
		parts = append(parts, fmt.Sprintf("go vet %s", p))
	}
	return strings.Join(parts, " && ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
