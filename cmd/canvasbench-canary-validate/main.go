package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
)

func main() {
	harness := flag.String("harness", "", "external harness name")
	input := flag.String("input", "", "captured harness JSON/JSONL path")
	expect := flag.String("expect", "CANVASBENCH_CANARY_OK", "expected normalized assistant output")
	flag.Parse()
	raw, err := os.ReadFile(*input)
	if err != nil {
		log.Fatal(err)
	}
	run, err := validateCanary(*harness, raw)
	if err != nil {
		log.Fatal(err)
	}
	if !strings.Contains(run.Output, *expect) {
		log.Fatalf("normalized output mismatch: got %q", run.Output)
	}
	fmt.Printf("canary=pass harness=%s session=%s\n", *harness, run.Trajectory.SessionID)
}

func validateCanary(harness string, raw []byte) (benchv2.HarnessRun, error) {
	normalizers := map[string]string{
		benchv2.AdapterOpenCode:  benchv2.NormalizerOpenCode,
		benchv2.AdapterQwenCode:  benchv2.NormalizerQwenCode,
		benchv2.AdapterMiniSWE:   benchv2.NormalizerMiniSWE,
		benchv2.AdapterPi:        benchv2.NormalizerPi,
		benchv2.AdapterAider:     benchv2.NormalizerAider,
		benchv2.AdapterOpenHands: benchv2.NormalizerOpenHands,
		benchv2.AdapterGoose:     benchv2.NormalizerGoose,
	}
	normalizer := normalizers[harness]
	if normalizer == "" {
		return benchv2.HarnessRun{}, fmt.Errorf("unsupported canary harness %q", harness)
	}
	execution := benchv2.OuterExecutionResult{
		Stdout: raw, ExitCode: 0, StartedAt: time.Unix(0, 0).UTC(), Duration: time.Second,
		Artifacts: map[string][]byte{},
	}
	if harness == benchv2.AdapterMiniSWE {
		execution.Stdout = nil
		execution.Artifacts[".canvasbench/mini-swe-agent-trajectory.json"] = raw
	}
	request := benchv2.TrialRequest{
		Task:    benchv2.TaskSpec{ID: "image-canary"},
		Harness: benchv2.HarnessConfig{Name: harness, Version: "canary", ConfigSHA256: "canary"},
		Runtime: benchv2.RuntimeAttestation{ModelID: "canvasbench-canary"},
	}
	run, err := benchv2.NormalizeExternal(harness, normalizer, request, execution)
	if err != nil {
		return run, err
	}
	if err := benchv2.ValidateATIF(run.Trajectory); err != nil {
		return run, err
	}
	return run, nil
}
