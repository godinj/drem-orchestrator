package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
)

func main() {
	matrixPath := flag.String("matrix", "bench/canvasbench-v2/matrices/example.json", "strict CanvasBench v2 matrix")
	manifestPath := flag.String("manifest", "bench/canvasbench-v2/manifest.json", "content-addressed CanvasBench v2 manifest")
	canvasRepo := flag.String("canvas-repo", "../drem-canvas.git/main", "Canvas source repository")
	orchRepo := flag.String("orchestrator-repo", ".", "orchestrator source repository")
	scratch := flag.String("scratch", "", "temporary worktree parent (defaults to OS temp)")
	out := flag.String("out", "bench/results/canvasbench-v2", "report prefix")
	endpoint := flag.String("endpoint", "http://127.0.0.1:18090/v1/chat/completions", "OpenAI-compatible inference endpoint")
	usageProxyAdminURL := flag.String("usage-proxy-admin-url", "", "host-reachable trusted usage proxy admin URL")
	usageProxyPublicBaseURL := flag.String("usage-proxy-public-base-url", "", "outer-harness-reachable trusted usage proxy /v1 base URL")
	usageProxyAdminTokenFile := flag.String("usage-proxy-admin-token-file", "", "owner-only file containing the trusted usage proxy admin token")
	flag.Parse()
	var matrix benchv2.MatrixSpec
	if err := benchv2.DecodeStrictFile(*matrixPath, &matrix); err != nil {
		log.Fatal(err)
	}
	if err := matrix.Validate(); err != nil {
		log.Fatal(err)
	}
	usageAttestor, err := usageAttestorForHarness(context.Background(), matrix.Harness, *usageProxyAdminURL, *usageProxyPublicBaseURL, *usageProxyAdminTokenFile)
	if err != nil {
		log.Fatal(err)
	}
	manifest, tasks, err := benchv2.LoadManifest(*manifestPath)
	if err != nil {
		log.Fatal(err)
	}
	if len(matrix.TaskFiles) != len(manifest.Cases) {
		log.Fatal("matrix must select every immutable manifest case exactly once")
	}
	for index, taskFile := range matrix.TaskFiles {
		if filepath.Base(taskFile) != filepath.Base(manifest.Cases[index].TaskFile) {
			log.Fatalf("matrix task %d does not match immutable manifest order", index+1)
		}
	}
	scratchRoot := *scratch
	if scratchRoot == "" {
		var err error
		scratchRoot, err = os.MkdirTemp("", "canvasbench-v2-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(scratchRoot)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	rawPath := *out + ".jsonl"
	_ = os.Remove(rawPath)
	var results []benchv2.TrialResult
	for _, task := range tasks {
		for trial := 1; trial <= matrix.Trials; trial++ {
			adapter, err := benchv2.SelectAdapter(matrix.Harness, task, *endpoint, usageAttestor)
			if err != nil {
				log.Fatal(err)
			}
			runner := benchv2.Runner{
				Repos:       map[string]string{"drem-canvas": *canvasRepo, "drem-orchestrator": *orchRepo},
				ScratchRoot: scratchRoot, Adapter: adapter,
				Verifier: benchv2.BuiltinVerifier{OracleRoot: filepath.Join(filepath.Dir(*manifestPath), "oracles")},
			}
			result := runner.RunTrial(context.Background(), matrix, task, trial)
			if err := benchv2.AppendJSONL(rawPath, result); err != nil {
				log.Fatal(err)
			}
			results = append(results, result)
			fmt.Printf("%s trial %d: %s score=%.1f\n", task.ID, trial, result.Status, result.Score)
		}
	}
	aggregate := benchv2.AggregateResults(matrix.ID, tasks, results)
	if err := benchv2.WriteReports(*out, aggregate, results); err != nil {
		log.Fatal(err)
	}
}

func usageAttestorForHarness(ctx context.Context, harness benchv2.HarnessConfig, adminURL, publicBaseURL, tokenFile string) (benchv2.ServerUsageAttestor, error) {
	switch harness.Name {
	case benchv2.AdapterOpenCode, benchv2.AdapterQwenCode, benchv2.AdapterMiniSWE, benchv2.AdapterPi, benchv2.AdapterAider, benchv2.AdapterOpenHands:
		if adminURL == "" || publicBaseURL == "" || tokenFile == "" {
			return nil, fmt.Errorf("external harness requires usage proxy admin URL, public base URL, and admin token file")
		}
		token, err := benchv2.ReadPrivateTokenFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read usage proxy admin token: %w", err)
		}
		client, err := benchv2.NewUsageProxyClient(benchv2.UsageProxyClientConfig{
			AdminURL: adminURL, PublicBaseURL: publicBaseURL, AdminToken: token,
			ExpectedAttestation: benchv2.UsageProxyAttestation{
				SourceState: harness.UsageProxySourceState,
				Image:       harness.UsageProxyImage, ConfigSHA256: harness.UsageProxyConfigSHA,
			},
		})
		if err != nil {
			return nil, err
		}
		if err := client.VerifyLiveAttestation(ctx); err != nil {
			return nil, err
		}
		return client, nil
	default:
		return nil, nil
	}
}
