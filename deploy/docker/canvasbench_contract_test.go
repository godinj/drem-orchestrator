package docker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type canvasBenchImageLock struct {
	Schema     string                      `json:"schema"`
	Platform   string                      `json:"platform"`
	BaseImages map[string]string           `json:"base_images"`
	Images     map[string]canvasBenchImage `json:"images"`
}

type canvasBenchImage struct {
	Version     string `json:"version"`
	Upstream    string `json:"upstream"`
	Integrity   string `json:"integrity"`
	Dockerfile  string `json:"dockerfile"`
	Executable  string `json:"executable"`
	EnvContract string `json:"env_contract"`
	Normalizer  string `json:"normalizer"`
}

func TestCanvasBenchImageBuildContractIsImmutableAndUnprivileged(t *testing.T) {
	root := repoRoot(t)
	lockPath := filepath.Join(root, "deploy", "docker", "canvasbench", "locks.json")
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock canvasBenchImageLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Schema != "canvasbench.images.v1" || lock.Platform != "linux/amd64" {
		t.Fatalf("unexpected lock identity: %#v", lock)
	}
	digest := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	for name, image := range lock.BaseImages {
		if !digest.MatchString(image) || strings.Contains(image, ":latest") {
			t.Fatalf("base %s is not immutably pinned: %s", name, image)
		}
	}
	canonical := []string{"usage-proxy", "opencode", "qwen-code", "mini-swe-agent", "pi", "aider", "openhands", "goose", "cline", "continue", "swe-agent"}
	for _, name := range canonical {
		image, ok := lock.Images[name]
		if !ok {
			t.Fatalf("missing image lock %s", name)
		}
		for field, value := range map[string]string{
			"version": image.Version, "upstream": image.Upstream, "dockerfile": image.Dockerfile,
			"executable": image.Executable, "env_contract": image.EnvContract,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s has empty %s", name, field)
			}
		}
		if name != "usage-proxy" && image.Integrity == "" {
			t.Fatalf("%s lacks upstream integrity", name)
		}
		dockerfile, err := os.ReadFile(filepath.Join(root, image.Dockerfile))
		if err != nil {
			t.Fatal(err)
		}
		content := string(dockerfile)
		for _, required := range []string{"USER 65532:65532", "io.drem.source-state", "io.drem.canvasbench.env-contract"} {
			if !strings.Contains(content, required) {
				t.Fatalf("%s Dockerfile lacks %q", name, required)
			}
		}
		if name == "opencode" && !strings.Contains(content, "node_modules/opencode-linux-x64-baseline/bin/opencode") {
			t.Fatal("OpenCode must install the locked portable linux/x64 binary after lifecycle scripts are globally disabled")
		}
		if strings.Contains(strings.ToLower(content), ":latest") || strings.Contains(content, "OPENAI_API_KEY=") {
			t.Fatalf("%s Dockerfile contains a floating input or baked credential", name)
		}
	}
}

func TestCanvasBenchWrappersImplementOnlyDeclaredEnvironmentContracts(t *testing.T) {
	root := repoRoot(t)
	paths := map[string][]string{
		"context/opencode/opencode-wrapper.mjs":  {"OPENAI_BASE_URL", "OPENAI_API_KEY", "@ai-sdk/openai-compatible"},
		"context/qwen-code/qwen-wrapper.sh":      {"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		"context/mini-swe-agent/mini-wrapper.sh": {"OPENAI_API_BASE", "OPENAI_API_KEY", "OPENAI_BASE_URL"},
		"context/pi/pi-wrapper.mjs":              {"OPENAI_BASE_URL", "OPENAI_API_KEY", "openai-completions"},
		"context/aider/aider-wrapper.py":         {"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_API_BASE"},
		"context/openhands/openhands-wrapper.py": {"OPENAI_BASE_URL", "OPENAI_API_KEY", "LLM_BASE_URL", "LLM_MODEL"},
		"context/goose/goose-wrapper.py":         {"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		"context/cline/cline-wrapper.mjs":        {"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		"context/continue/continue-wrapper.mjs":  {"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		"context/swe-agent/swe-agent-wrapper.py": {"OPENAI_BASE_URL", "OPENAI_API_KEY", "type\": \"local"},
	}
	base := filepath.Join(root, "deploy", "docker", "canvasbench")
	for relative, required := range paths {
		raw, err := os.ReadFile(filepath.Join(base, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range required {
			if !strings.Contains(string(raw), token) {
				t.Fatalf("%s lacks contract token %q", relative, token)
			}
		}
	}
}

func TestCanvasBenchImageToolsWithFakeDocker(t *testing.T) {
	root := repoRoot(t)
	command := exec.Command("sh", filepath.Join(root, "deploy", "docker", "canvasbench", "test-tools.sh"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("image tool structural test failed: %v\n%s", err, output)
	}
}

func TestCanvasBenchFakeOpenAIProvidesMeasuredStreamingAndNonStreamingResponses(t *testing.T) {
	root := repoRoot(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, "python3", filepath.Join(root, "deploy", "docker", "canvasbench", "fake_openai.py"))
	command.Env = append(os.Environ(), "CANVASBENCH_FAKE_LISTEN="+address)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()

	endpoint := "http://" + address + "/v1/chat/completions"
	policy := `"model":"canvasbench-canary-runtime","seed":42,"temperature":0.2,"top_p":0.9,"top_k":20,"max_tokens":1024,"chat_template_kwargs":{"preserve_thinking":true}`
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{`+policy+`}`))
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := client.Do(request)
		if requestErr == nil {
			defer response.Body.Close()
			var payload struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Choices[0].Message.Content != "CANVASBENCH_CANARY_OK" || payload.Usage.PromptTokens != 17 || payload.Usage.CompletionTokens != 4 {
				t.Fatalf("unexpected fake response: %#v", payload)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake upstream did not start: %v\n%s", requestErr, logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	for _, test := range []struct {
		body       string
		statusCode int
		contains   string
	}{
		{`{` + policy + `,"stream":true,"stream_options":{"include_usage":true}}`, http.StatusOK, `"prompt_tokens": 17`},
		{`{` + policy + `,"stream":true}`, http.StatusUnprocessableEntity, "streaming usage was not requested"},
	} {
		request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(test.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != test.statusCode || !strings.Contains(string(raw), test.contains) {
			t.Fatal(fmt.Sprintf("unexpected stream response status=%d body=%s", response.StatusCode, raw))
		}
	}
}
