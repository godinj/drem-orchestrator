package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/agentmon"
)

// ---------------------------------------------------------------------------
// TestGenerateExitLogScript
// ---------------------------------------------------------------------------

func TestGenerateExitLogScript(t *testing.T) {
	agentID := uuid.New()
	taskID := uuid.New()
	projectDir := t.TempDir()

	script := generateExitLogScript(agentID, taskID, projectDir)

	t.Run("starts with shebang", func(t *testing.T) {
		if !strings.HasPrefix(script, "#!/bin/bash") {
			prefix := script
			if len(prefix) > 40 {
				prefix = prefix[:40]
			}
			t.Errorf("script should start with #!/bin/bash, got prefix: %q", prefix)
		}
	})

	t.Run("references agent-metadata.json", func(t *testing.T) {
		if !strings.Contains(script, "agent-metadata.json") {
			t.Error("script should read agent-metadata.json for agent_id and task_id")
		}
	})

	t.Run("reads jsonl transcript", func(t *testing.T) {
		if !strings.Contains(script, ".jsonl") {
			t.Error("script should reference .jsonl transcript file")
		}
	})

	t.Run("extracts tool_use entry", func(t *testing.T) {
		if !strings.Contains(script, "tool_use") {
			t.Error("script should extract tool_use entries from transcript")
		}
	})

	t.Run("writes to exit-log.jsonl", func(t *testing.T) {
		if !strings.Contains(script, "exit-log.jsonl") {
			t.Error("script should write output to exit-log.jsonl")
		}
	})

	t.Run("contains project dir path", func(t *testing.T) {
		if !strings.Contains(script, projectDir) {
			t.Errorf("script should reference project dir %q", projectDir)
		}
	})

	t.Run("contains agent and task IDs", func(t *testing.T) {
		if !strings.Contains(script, agentID.String()) {
			t.Errorf("script should embed agent ID %s", agentID)
		}
		if !strings.Contains(script, taskID.String()) {
			t.Errorf("script should embed task ID %s", taskID)
		}
	})
}

// ---------------------------------------------------------------------------
// TestWriteAgentMetadata
// ---------------------------------------------------------------------------

func TestWriteAgentMetadata(t *testing.T) {
	agentID := uuid.New()
	taskID := uuid.New()
	claudeDir := t.TempDir()

	err := writeAgentMetadata(claudeDir, agentID, taskID)
	if err != nil {
		t.Fatalf("writeAgentMetadata returned error: %v", err)
	}

	t.Run("creates metadata file", func(t *testing.T) {
		metaPath := filepath.Join(claudeDir, "agent-metadata.json")
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			t.Fatal("agent-metadata.json was not created")
		}
	})

	t.Run("contains correct JSON", func(t *testing.T) {
		metaPath := filepath.Join(claudeDir, "agent-metadata.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("read metadata: %v", err)
		}

		var meta map[string]string
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}

		if meta["agent_id"] != agentID.String() {
			t.Errorf("agent_id = %q, want %q", meta["agent_id"], agentID.String())
		}
		if meta["task_id"] != taskID.String() {
			t.Errorf("task_id = %q, want %q", meta["task_id"], taskID.String())
		}
	})

	t.Run("contains only expected keys", func(t *testing.T) {
		metaPath := filepath.Join(claudeDir, "agent-metadata.json")
		data, _ := os.ReadFile(metaPath)

		var meta map[string]any
		json.Unmarshal(data, &meta)

		if len(meta) != 2 {
			t.Errorf("metadata should have exactly 2 keys, got %d: %v", len(meta), meta)
		}
	})
}

func TestWriteAgentMetadata_InvalidDir(t *testing.T) {
	err := writeAgentMetadata("/nonexistent/path", uuid.New(), uuid.New())
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

// ---------------------------------------------------------------------------
// TestSettingsJSONIncludesStopHook
// ---------------------------------------------------------------------------

func TestSettingsJSONIncludesStopHook(t *testing.T) {
	claudeDir := t.TempDir()
	worktreePath := t.TempDir()
	exitLogScriptPath := filepath.Join(claudeDir, "exit-log.sh")

	// Write a placeholder exit log script so path exists.
	os.WriteFile(exitLogScriptPath, []byte("#!/bin/bash\n"), 0o755)

	// Build existing hooks (Notification + PreCompact) as runner.go would.
	existingHooks := map[string]any{
		"Notification": []any{
			map[string]any{
				"matcher": "idle_prompt",
				"hooks": []any{
					map[string]any{"type": "command", "command": "touch idle", "timeout": 5},
				},
			},
		},
		"PreCompact": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "command": "touch compact", "timeout": 5},
				},
			},
		},
	}

	settings := buildSettingsJSON(claudeDir, worktreePath, exitLogScriptPath, existingHooks)

	// Parse the settings to inspect hooks.
	settingsBytes, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(settingsBytes, &parsed); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatal("settings should contain a 'hooks' key with map value")
	}

	t.Run("has Stop hook", func(t *testing.T) {
		if _, ok := hooks["Stop"]; !ok {
			t.Error("hooks should include a 'Stop' entry")
		}
	})

	t.Run("Stop hook references exit-log script", func(t *testing.T) {
		stopHooks, ok := hooks["Stop"]
		if !ok {
			t.Skip("no Stop hook found")
		}
		stopBytes, _ := json.Marshal(stopHooks)
		stopStr := string(stopBytes)
		if !strings.Contains(stopStr, exitLogScriptPath) {
			t.Errorf("Stop hook should reference exit-log script path %q, got %s", exitLogScriptPath, stopStr)
		}
	})

	t.Run("preserves Notification hook", func(t *testing.T) {
		if _, ok := hooks["Notification"]; !ok {
			t.Error("hooks should still include 'Notification' entry")
		}
	})

	t.Run("preserves PreCompact hook", func(t *testing.T) {
		if _, ok := hooks["PreCompact"]; !ok {
			t.Error("hooks should still include 'PreCompact' entry")
		}
	})

	t.Run("Stop hook has correct matcher-hooks structure", func(t *testing.T) {
		stopRaw, ok := hooks["Stop"]
		if !ok {
			t.Skip("no Stop hook found")
		}
		stopArr, ok := stopRaw.([]any)
		if !ok || len(stopArr) == 0 {
			t.Fatalf("Stop hook should be a non-empty array, got %T", stopRaw)
		}
		matcher, ok := stopArr[0].(map[string]any)
		if !ok {
			t.Fatalf("Stop hook entry should be an object, got %T", stopArr[0])
		}
		hooksArr, ok := matcher["hooks"].([]any)
		if !ok || len(hooksArr) == 0 {
			t.Fatalf("Stop hook entry must have a 'hooks' array, got keys: %v", matcher)
		}
		cmd, ok := hooksArr[0].(map[string]any)
		if !ok {
			t.Fatalf("hooks[0] should be an object, got %T", hooksArr[0])
		}
		if cmd["type"] != "command" {
			t.Errorf("hook type = %v, want \"command\"", cmd["type"])
		}
		if cmd["command"] != exitLogScriptPath {
			t.Errorf("hook command = %v, want %q", cmd["command"], exitLogScriptPath)
		}
	})
}

// ---------------------------------------------------------------------------
// TestEnrichCompletionWithExitLog
// ---------------------------------------------------------------------------

func TestEnrichCompletionWithExitLog(t *testing.T) {
	agentID := uuid.New()
	taskID := uuid.New()
	projectDir := t.TempDir()

	// Write a fake exit-log.jsonl with an entry for this agent.
	entry := agentmon.ExitLogEntry{
		AgentID:    agentID.String(),
		TaskID:     taskID.String(),
		LastTool:   "Edit",
		LastTarget: "internal/agent/runner.go",
		ExitReason: "success",
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal exit log entry: %v", err)
	}
	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	if err := os.WriteFile(exitLogPath, append(entryBytes, '\n'), 0o644); err != nil {
		t.Fatalf("write exit log: %v", err)
	}

	comp := &Completion{
		AgentID:    agentID,
		ReturnCode: 0,
	}

	enrichCompletion(comp, projectDir)

	t.Run("populates ExitInfo", func(t *testing.T) {
		if comp.ExitInfo == nil {
			t.Fatal("ExitInfo should be populated from exit-log.jsonl")
		}
	})

	t.Run("ExitInfo has correct last tool", func(t *testing.T) {
		if comp.ExitInfo == nil {
			t.Skip("ExitInfo is nil")
		}
		if comp.ExitInfo.LastTool != "Edit" {
			t.Errorf("ExitInfo.LastTool = %q, want %q", comp.ExitInfo.LastTool, "Edit")
		}
	})

	t.Run("ExitInfo has correct exit reason", func(t *testing.T) {
		if comp.ExitInfo == nil {
			t.Skip("ExitInfo is nil")
		}
		if comp.ExitInfo.ExitReason != "success" {
			t.Errorf("ExitInfo.ExitReason = %q, want %q", comp.ExitInfo.ExitReason, "success")
		}
	})
}

func TestEnrichCompletionWithExitLog_NoFile(t *testing.T) {
	comp := &Completion{
		AgentID:    uuid.New(),
		ReturnCode: 1,
	}

	// No exit-log.jsonl exists in this dir.
	enrichCompletion(comp, t.TempDir())

	if comp.ExitInfo != nil {
		t.Error("ExitInfo should be nil when no exit-log.jsonl exists")
	}
}

func TestEnrichCompletionWithExitLog_MultipleEntries(t *testing.T) {
	agentID := uuid.New()
	otherAgentID := uuid.New()
	projectDir := t.TempDir()

	// Write multiple entries — only the one matching agentID should be returned.
	entries := []agentmon.ExitLogEntry{
		{AgentID: otherAgentID.String(), TaskID: uuid.New().String(), LastTool: "Bash", ExitReason: "error"},
		{AgentID: agentID.String(), TaskID: uuid.New().String(), LastTool: "Read", ExitReason: "context_limit"},
		{AgentID: agentID.String(), TaskID: uuid.New().String(), LastTool: "Write", ExitReason: "success"},
	}

	var lines []byte
	for _, e := range entries {
		b, _ := json.Marshal(e)
		lines = append(lines, b...)
		lines = append(lines, '\n')
	}

	exitLogPath := filepath.Join(projectDir, "exit-log.jsonl")
	os.WriteFile(exitLogPath, lines, 0o644)

	comp := &Completion{AgentID: agentID, ReturnCode: 0}
	enrichCompletion(comp, projectDir)

	if comp.ExitInfo == nil {
		t.Fatal("ExitInfo should be populated")
	}
	// Should pick the LATEST matching entry for this agent.
	if comp.ExitInfo.LastTool != "Write" {
		t.Errorf("should return latest entry: LastTool = %q, want %q", comp.ExitInfo.LastTool, "Write")
	}
	if comp.ExitInfo.ExitReason != "success" {
		t.Errorf("should return latest entry: ExitReason = %q, want %q", comp.ExitInfo.ExitReason, "success")
	}
}
