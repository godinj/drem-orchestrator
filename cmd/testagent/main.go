package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/godinj/drem-orchestrator/internal/agent"
)

func main() {
	cfg := agent.DefaultDirectToolAgentConfig()
	cfg.Endpoint = "http://localhost:8081/v1/chat/completions"
	cfg.Model = "gemma4-26b"
	cfg.MaxTokens = 8192
	cfg.MaxIterations = 15
	cfg.WorkDir = "/home/godinj/git/drem-orchestrator.git/master"
	cfg.TraceWriter = os.Stderr

	system := `You are a coder agent. Your task: Write tests for EndpointHealthChecker.

## Working Directory
/home/godinj/git/drem-orchestrator.git/master

## MANDATORY EXECUTION SEQUENCE
STEP 1: Read ONE source file (the main file you need to understand).
STEP 2: Write your code using write_file. Write the COMPLETE file content.
STEP 3: Run go vet ./... && go test ./...
STEP 4: If tests fail, fix and re-run ONCE.
STEP 5: Run git add -A && git commit -m '<message>'

RULES:
- You have MAX 15 tool calls.
- Do NOT run ls, find, or grep to explore.
- Do NOT read more than 2 files before writing code.
- WRITE CODE on your 2nd or 3rd tool call. Not later.
- If you have not called write_file by your 4th tool call, you are FAILING.

Test Infrastructure:
- DB: use testutil.NewTestDB(t), never gorm.Open directly.
- Shared helpers: internal/testutil/testutil.go`

	user := "Write tests for the EndpointHealthChecker in internal/agent/endpoint_health.go. Read that file first, then write tests."

	result, err := agent.RunDirectToolAgent(cfg, system, user, agent.ToolsForRole("coder"), "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nERROR: %v\n", err)
	}
	if result != nil {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(b))
	}
}
