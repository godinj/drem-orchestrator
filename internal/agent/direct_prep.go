// direct_prep.go implements a read-only tool-calling agent that performs
// reconnaissance on a subtask's target files before the coder agent writes.
// It reuses the DirectToolAgent infrastructure but restricts the toolset to
// {read, grep, glob} via ToolsForRole("prep").
//
// Output contract: the model must emit a final JSON object matching the
// orchestrator.PrepOutput shape (target_files, insertion_points,
// patterns_to_follow, warnings, constructors). The RunDirectPrep caller
// is responsible for unmarshaling that JSON — this file deliberately does
// not import the orchestrator package to avoid a cycle.
package agent

import (
	"fmt"
	"strings"
)

// DirectPrepConfig holds connection and generation parameters for the
// direct SGLang prep agent. It embeds the base tool-calling agent config.
type DirectPrepConfig struct {
	DirectToolAgentConfig
}

// PrepPromptOpts contains the context needed to build a prep system prompt.
type PrepPromptOpts struct {
	TaskTitle         string
	TaskDescription   string
	EstimatedFiles    []string
	WorkDir           string
	ParentTitle       string
	ParentDescription string
	PlanJSON          string
}

// prepSystemPromptTemplate is the base instruction block for the prep agent.
// It defines the read-only mandate, the tool usage policy, and the exact
// JSON output schema the model must return as its final assistant message.
const prepSystemPromptTemplate = `You are a prep agent. Your job is read-only reconnaissance on a codebase so a subsequent coder agent has a tactical brief before writing any code.

You are READ-ONLY. You have three tools and three tools only:
- read: read file contents by path (optionally offset/limit)
- grep: regex search across the repo (optionally glob-filtered)
- glob: list files matching a glob pattern

You MUST NOT attempt to modify any file. The toolset is deliberately restricted to reads and searches: you cannot edit, cannot write, and cannot run shell commands. If you need to verify something, use read or grep.

Your output contract is strict. Your FINAL assistant message (the one where you stop calling tools) MUST be a single JSON object matching this schema exactly:

{
  "target_files": [
    {
      "path": "relative/path/to/file.go",
      "relevant_definitions": "type X struct { ... } or func Y(...) ...",
      "methods": ["MethodA", "MethodB"],
      "notes": "what the coder needs to know about this file"
    }
  ],
  "insertion_points": [
    {
      "file": "relative/path.go",
      "location": "after line N / inside func Foo / at the end of the struct",
      "what": "short description of what to insert"
    }
  ],
  "patterns_to_follow": [
    {
      "description": "how this codebase does X",
      "example": "short code snippet illustrating the pattern",
      "source_file": "where you saw it"
    }
  ],
  "warnings": [
    "anything the coder should watch out for: shared state, ordering, tests that will break, etc."
  ],
  "constructors": [
    {
      "struct_name": "Foo",
      "constructor": "NewFoo",
      "test_helpers": ["newTestFoo", "makeFoo"]
    }
  ]
}

All fields are required. Empty arrays are fine when a category has nothing to report. Do not wrap the JSON in markdown fences. Do not add prose before or after the JSON in your final message.

Recon procedure:
1. read each estimated target file end to end so you understand its current shape.
2. grep for struct definitions, constructor functions, and caller sites relevant to the change.
3. glob to find sibling or test files the coder will likely need to touch.
4. When you have enough signal, emit the PrepOutput JSON and stop.

Budget: prefer narrow, targeted tool calls over broad ones. Aim for under 20 tool calls total.`

// PrepSystemPrompt builds the system prompt for the prep agent, weaving in
// the specific subtask title/description, the estimated target files, and
// parent-task context when available.
func PrepSystemPrompt(opts PrepPromptOpts) string {
	var b strings.Builder
	b.WriteString(prepSystemPromptTemplate)
	b.WriteString("\n\n")

	b.WriteString("## Subtask you are prepping for\n\n")
	if opts.TaskTitle != "" {
		fmt.Fprintf(&b, "Title: %s\n", opts.TaskTitle)
	}
	if opts.TaskDescription != "" {
		fmt.Fprintf(&b, "Description:\n%s\n", opts.TaskDescription)
	}

	if len(opts.EstimatedFiles) > 0 {
		b.WriteString("\n## Estimated target files\n\n")
		b.WriteString("Start your recon by reading these files (paths are relative to the feature worktree):\n\n")
		for _, f := range opts.EstimatedFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	if opts.ParentTitle != "" || opts.ParentDescription != "" {
		b.WriteString("\n## Parent task context\n\n")
		if opts.ParentTitle != "" {
			fmt.Fprintf(&b, "Parent title: %s\n", opts.ParentTitle)
		}
		if opts.ParentDescription != "" {
			fmt.Fprintf(&b, "Parent description:\n%s\n", opts.ParentDescription)
		}
	}

	if opts.PlanJSON != "" {
		b.WriteString("\n## Full plan (parent decomposition)\n\n")
		b.WriteString("```json\n")
		b.WriteString(opts.PlanJSON)
		b.WriteString("\n```\n")
	}

	if opts.WorkDir != "" {
		b.WriteString("\n## Working directory\n\n")
		fmt.Fprintf(&b, "All tool paths resolve relative to: %s\n", opts.WorkDir)
	}

	b.WriteString("\nProduce the PrepOutput JSON and stop.\n")
	return b.String()
}

// buildPrepUserMessage returns the first user-role message sent to the model.
// Keeping this narrow lets the system prompt carry the heavy context and the
// user turn act as a simple go-signal that references the task by title.
func buildPrepUserMessage(opts PrepPromptOpts) string {
	var b strings.Builder
	if opts.TaskTitle != "" {
		fmt.Fprintf(&b, "Run recon for: %s\n\n", opts.TaskTitle)
	} else {
		b.WriteString("Run recon for the subtask described in the system prompt.\n\n")
	}
	if opts.TaskDescription != "" {
		b.WriteString(opts.TaskDescription)
		b.WriteString("\n\n")
	}
	if len(opts.EstimatedFiles) > 0 {
		b.WriteString("Estimated files to inspect:\n")
		for _, f := range opts.EstimatedFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	b.WriteString("Emit PrepOutput JSON when done.")
	return b.String()
}

// RunDirectPrep calls the SGLang tool-calling agent in prep mode. It returns
// the run result (token usage, iteration count, duration, and raw output
// text). The caller is responsible for parsing the output file as
// PrepOutput — this function intentionally does not couple to the
// orchestrator-layer type.
func RunDirectPrep(cfg DirectPrepConfig, opts PrepPromptOpts, outputPath string) (*DirectToolAgentResult, error) {
	systemPrompt := PrepSystemPrompt(opts)
	userMessage := buildPrepUserMessage(opts)
	tools := ToolsForRole("prep")
	return RunDirectToolAgent(cfg.DirectToolAgentConfig, systemPrompt, userMessage, tools, outputPath)
}
