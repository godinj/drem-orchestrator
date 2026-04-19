package extract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedNow is the synthetic timestamp injected into every test so that
// assertions on Event.Timestamp are exact rather than approximate.
var fixedNow = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

func TestParseLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		line   string
		verify func(t *testing.T, ev Event)
	}{
		{
			name: "commit summary recognised from git stdout",
			src:  "stdout",
			line: "[master 1a2b3c4] feat: add thing",
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Commit)
				require.True(t, ok, "expected *Commit, got %T", ev)
				require.Equal(t, "master", c.Branch)
				require.Equal(t, "1a2b3c4", c.SHA)
				require.Equal(t, "feat: add thing", c.Message)
				require.Equal(t, "stdout", c.Source)
				require.Equal(t, fixedNow, c.Timestamp)
			},
		},
		{
			name: "commit summary with slashes and long sha",
			src:  "stdout",
			line: "[feature/foo-bar 1234567890abcdef1234567890abcdef12345678] Fix things",
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Commit)
				require.True(t, ok)
				require.Equal(t, "feature/foo-bar", c.Branch)
				require.Equal(t, "1234567890abcdef1234567890abcdef12345678", c.SHA)
				require.Equal(t, "Fix things", c.Message)
			},
		},
		{
			name: "echoed git commit command carries message",
			src:  "stdout",
			line: `+ git commit -m "fix the bug"`,
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Commit)
				require.True(t, ok)
				require.Equal(t, "fix the bug", c.Message)
				require.Empty(t, c.SHA)
			},
		},
		{
			name: "push header gives remote",
			src:  "stdout",
			line: "To github.com:example/repo.git",
			verify: func(t *testing.T, ev Event) {
				p, ok := ev.(*Push)
				require.True(t, ok, "expected *Push, got %T", ev)
				require.Equal(t, "github.com:example/repo.git", p.Remote)
			},
		},
		{
			name: "push ref line gives branch",
			src:  "stdout",
			line: "   abc1234..def5678  feature/foo -> feature/foo",
			verify: func(t *testing.T, ev Event) {
				p, ok := ev.(*Push)
				require.True(t, ok)
				require.Equal(t, "feature/foo", p.Branch)
			},
		},
		{
			name: "echoed git push carries remote and branch",
			src:  "stdout",
			line: "+ git push origin feature/foo",
			verify: func(t *testing.T, ev Event) {
				p, ok := ev.(*Push)
				require.True(t, ok)
				require.Equal(t, "origin", p.Remote)
				require.Equal(t, "feature/foo", p.Branch)
			},
		},
		{
			name: "go test ok line produces passing TestResult",
			src:  "stdout",
			line: "ok  \tgithub.com/foo/bar\t0.123s",
			verify: func(t *testing.T, ev Event) {
				tr, ok := ev.(*TestResult)
				require.True(t, ok, "expected *TestResult, got %T", ev)
				require.True(t, tr.Passed)
				require.Contains(t, tr.Summary, "github.com/foo/bar")
			},
		},
		{
			name: "go test FAIL package line produces failing TestResult",
			src:  "stdout",
			line: "FAIL\tgithub.com/foo/bar\t0.456s",
			verify: func(t *testing.T, ev Event) {
				tr, ok := ev.(*TestResult)
				require.True(t, ok)
				require.False(t, tr.Passed)
				require.Contains(t, tr.Summary, "github.com/foo/bar")
			},
		},
		{
			name: "--- FAIL subtest line produces failing TestResult",
			src:  "stdout",
			line: "--- FAIL: TestSomething (0.01s)",
			verify: func(t *testing.T, ev Event) {
				tr, ok := ev.(*TestResult)
				require.True(t, ok)
				require.False(t, tr.Passed)
				require.Equal(t, "TestSomething", tr.Summary)
			},
		},
		{
			name: "--- PASS subtest line produces passing TestResult",
			src:  "stdout",
			line: "--- PASS: TestSomething (0.01s)",
			verify: func(t *testing.T, ev Event) {
				tr, ok := ev.(*TestResult)
				require.True(t, ok)
				require.True(t, tr.Passed)
				require.Equal(t, "TestSomething", tr.Summary)
			},
		},
		{
			name: "go compiler error produces BuildError with file and line",
			src:  "stdout",
			line: "foo.go:12:3: undefined: bar",
			verify: func(t *testing.T, ev Event) {
				be, ok := ev.(*BuildError)
				require.True(t, ok, "expected *BuildError, got %T", ev)
				require.Equal(t, "go", be.Tool)
				require.Equal(t, "foo.go", be.File)
				require.Equal(t, 12, be.Line)
				require.Equal(t, "undefined: bar", be.Message)
			},
		},
		{
			name: "C++ compiler error produces BuildError with cpp tool",
			src:  "stdout",
			line: "main.cpp:15:8: error: 'foo' was not declared in this scope",
			verify: func(t *testing.T, ev Event) {
				be, ok := ev.(*BuildError)
				require.True(t, ok)
				require.Equal(t, "cpp", be.Tool)
				require.Equal(t, "main.cpp", be.File)
				require.Equal(t, 15, be.Line)
				require.Contains(t, be.Message, "'foo' was not declared")
			},
		},
		{
			name: "C compiler error produces BuildError with cpp tool",
			src:  "stdout",
			line: "src/lib.c:42:5: error: expected ';' before '}' token",
			verify: func(t *testing.T, ev Event) {
				be, ok := ev.(*BuildError)
				require.True(t, ok)
				require.Equal(t, "cpp", be.Tool)
				require.Equal(t, "src/lib.c", be.File)
				require.Equal(t, 42, be.Line)
			},
		},
		{
			name: "cargo error produces BuildError with cargo tool",
			src:  "stdout",
			line: "error[E0425]: cannot find value `foo` in this scope",
			verify: func(t *testing.T, ev Event) {
				be, ok := ev.(*BuildError)
				require.True(t, ok)
				require.Equal(t, "cargo", be.Tool)
				require.Contains(t, be.Message, "cannot find value")
			},
		},
		{
			name: "watchdog heartbeat produces Heartbeat with agent id",
			src:  "stdout",
			line: "DREM-HEARTBEAT agent_id=worker-42",
			verify: func(t *testing.T, ev Event) {
				hb, ok := ev.(*Heartbeat)
				require.True(t, ok, "expected *Heartbeat, got %T", ev)
				require.Equal(t, "worker-42", hb.AgentID)
				require.Equal(t, "stdout", hb.Source)
			},
		},
		{
			name: "heartbeat with extra kv fields still parses agent id",
			src:  "stdout",
			line: "DREM-HEARTBEAT agent_id=worker-7 uptime=30s",
			verify: func(t *testing.T, ev Event) {
				hb, ok := ev.(*Heartbeat)
				require.True(t, ok)
				require.Equal(t, "worker-7", hb.AgentID)
			},
		},
		{
			name: "panic line produces Crash",
			src:  "stdout",
			line: "panic: runtime error: index out of range [5] with length 3",
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Crash)
				require.True(t, ok, "expected *Crash, got %T", ev)
				require.Contains(t, c.Reason, "panic:")
				require.Equal(t, 0, c.ExitCode)
			},
		},
		{
			name: "SIGSEGV signal produces Crash",
			src:  "stdout",
			line: "[signal SIGSEGV: segmentation violation code=0x1]",
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Crash)
				require.True(t, ok)
				require.Contains(t, c.Reason, "SIGSEGV")
			},
		},
		{
			name: "non-zero exit status produces Crash with code",
			src:  "stdout",
			line: "exit status 2",
			verify: func(t *testing.T, ev Event) {
				c, ok := ev.(*Crash)
				require.True(t, ok)
				require.Equal(t, 2, c.ExitCode)
				require.Equal(t, "exit status 2", c.Reason)
			},
		},
		{
			name: "zero exit status is not a crash",
			src:  "stdout",
			line: "exit status 0",
			verify: func(t *testing.T, ev Event) {
				require.Nil(t, ev)
			},
		},
		{
			name: "assistant tool_use transcript line produces ToolCall",
			src:  "transcript",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/u/foo.go"}}]}}`,
			verify: func(t *testing.T, ev Event) {
				tc, ok := ev.(*ToolCall)
				require.True(t, ok, "expected *ToolCall, got %T", ev)
				require.Equal(t, "Read", tc.Tool)
				require.Equal(t, "/home/u/foo.go", tc.Target)
				require.Equal(t, "transcript", tc.Source)
			},
		},
		{
			name: "assistant Bash tool_use uses description as target",
			src:  "transcript",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"description":"Run tests","command":"go test ./..."}}]}}`,
			verify: func(t *testing.T, ev Event) {
				tc, ok := ev.(*ToolCall)
				require.True(t, ok)
				require.Equal(t, "Bash", tc.Tool)
				require.Equal(t, "Run tests", tc.Target)
			},
		},
		{
			name: "unrelated prose returns nil",
			src:  "stdout",
			line: "Hello, world! This is just a log line.",
			verify: func(t *testing.T, ev Event) {
				require.Nil(t, ev)
			},
		},
		{
			name: "empty line returns nil",
			src:  "stdout",
			line: "",
			verify: func(t *testing.T, ev Event) {
				require.Nil(t, ev)
			},
		},
		{
			name: "blank whitespace line returns nil",
			src:  "stdout",
			line: "   \t  ",
			verify: func(t *testing.T, ev Event) {
				require.Nil(t, ev)
			},
		},
		{
			name: "non-assistant JSON returns nil",
			src:  "transcript",
			line: `{"type":"human","message":{"content":"hello"}}`,
			verify: func(t *testing.T, ev Event) {
				require.Nil(t, ev)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseLine(tc.src, []byte(tc.line), fixedNow)
			tc.verify(t, got)
		})
	}
}

func TestParseLine_TrimsTrailingNewline(t *testing.T) {
	t.Parallel()
	ev := ParseLine("stdout", []byte("[master 1a2b3c4] feat: x\n"), fixedNow)
	c, ok := ev.(*Commit)
	require.True(t, ok)
	require.Equal(t, "feat: x", c.Message)
}

func TestParseLine_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []byte("[master 1a2b3c4] feat: x")
	before := string(in)
	_ = ParseLine("stdout", in, fixedNow)
	require.Equal(t, before, string(in))
}
