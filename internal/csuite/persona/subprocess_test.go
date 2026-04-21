package persona

// subprocess_test.go exercises the os/exec-backed Spawner returned by
// NewClaudeSpawner. The tests stand up a tiny shell-script "claude" in a
// tempdir and assert the diagnostic-capture contract:
//
//   - On a clean non-zero exit, stderr is folded into the returned
//     stdout bytes so the poller's recordFailure path surfaces it.
//   - On a launch failure (binary not found), stderr (empty in that
//     case) is still threaded through the wrapped error without
//     panicking.
//   - truncateStderr caps runaway stderr at the stated limit.
//
// Tests run in package persona (white-box) so they can reach the
// unexported spawnClaude and appendDiagnostic helpers directly.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeStubClaudeScript drops a shell script named "claude" into dir
// whose stdout, stderr, and exit-code are controlled by the test.
func writeStubClaudeScript(t *testing.T, dir, stdoutLine, stderrLine string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub unsupported on windows")
	}
	path := filepath.Join(dir, "claude")
	// printf %s avoids accidental backslash interpretation in the user
	// string; single-quotes survive through the Go literal.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"" + stdoutLine + "\"\n" +
		"printf '%s\\n' \"" + stderrLine + "\" 1>&2\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func itoa(n int) string {
	// tiny helper to avoid importing strconv just for this
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestSpawnClaude_CapturesStderrOnNonZeroExit asserts stderr appears in
// the returned stdout bytes when the process exits non-zero. Prior to
// this change cmd.Stderr=nil sent stderr to /dev/null so the poller's
// recordFailure reason was always the empty stdout (or "non-zero exit").
func TestSpawnClaude_CapturesStderrOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	stub := writeStubClaudeScript(t, dir, "", "unknown option '---\\nfrom: alex\\n---'", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdout, exitCode, err := spawnClaude(ctx, []string{stub}, nil)
	if err != nil {
		t.Fatalf("want err=nil on clean non-zero exit, got %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("want exitCode=1, got %d", exitCode)
	}
	if !strings.Contains(string(stdout), "unknown option") {
		t.Fatalf("captured bytes must contain stderr diagnostic; got %q", string(stdout))
	}
	if !strings.Contains(string(stdout), "stderr:") {
		t.Fatalf("captured bytes must prefix stderr payload with 'stderr:' so operators can tell it apart from real stdout; got %q", string(stdout))
	}
}

// TestSpawnClaude_LaunchFailureWrapsStderrInError covers the branch
// where the subprocess could not be launched (binary missing). The
// wrapped error should contain the stderr=... segment even if stderr is
// empty — the format must be stable so log greps keep working.
func TestSpawnClaude_LaunchFailureWrapsStderrInError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdout, exitCode, err := spawnClaude(ctx, []string{"/no/such/binary-definitely-missing"}, nil)
	if err == nil {
		t.Fatalf("want non-nil err on missing binary, got stdout=%q exit=%d", stdout, exitCode)
	}
	if exitCode != -1 {
		t.Fatalf("want exitCode=-1 on launch failure, got %d", exitCode)
	}
	if !strings.Contains(err.Error(), "stderr=") {
		t.Fatalf("err must carry stderr=... segment for grepability; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "launch failed") {
		t.Fatalf("err must identify the failure mode; got %q", err.Error())
	}
}

// TestSpawnClaude_HappyPath asserts the fix did not regress the clean
// zero-exit case: stdout bytes are returned verbatim, err is nil, and
// exitCode is 0.
func TestSpawnClaude_HappyPath(t *testing.T) {
	dir := t.TempDir()
	stub := writeStubClaudeScript(t, dir, "hello from claude", "", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdout, exitCode, err := spawnClaude(ctx, []string{stub}, nil)
	if err != nil {
		t.Fatalf("happy path err: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("happy path exitCode: want 0, got %d", exitCode)
	}
	if !strings.Contains(string(stdout), "hello from claude") {
		t.Fatalf("want stdout to carry reply verbatim; got %q", string(stdout))
	}
	if strings.Contains(string(stdout), "stderr:") {
		t.Fatalf("happy-path stdout must not be polluted with stderr prefix; got %q", string(stdout))
	}
}

// TestTruncateStderr_CapsAtLimit verifies the 4 KiB cap trims correctly
// and the truncated-sentinel is appended so operators can see they lost
// bytes.
func TestTruncateStderr_CapsAtLimit(t *testing.T) {
	big := strings.Repeat("x", stderrCaptureLimit+500)
	got := truncateStderr(big, stderrCaptureLimit)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("want truncated sentinel, got suffix %q", got[len(got)-20:])
	}
	if len(got) != stderrCaptureLimit+len("...(truncated)") {
		t.Fatalf("want len=%d, got %d", stderrCaptureLimit+len("...(truncated)"), len(got))
	}
}

// TestTruncateStderr_PreservesShortPayload asserts short stderr passes
// through untouched (aside from whitespace trim), so the common case
// produces a clean error message.
func TestTruncateStderr_PreservesShortPayload(t *testing.T) {
	in := "  unknown option '---'\n"
	got := truncateStderr(in, stderrCaptureLimit)
	if got != "unknown option '---'" {
		t.Fatalf("want trim+passthrough, got %q", got)
	}
}
