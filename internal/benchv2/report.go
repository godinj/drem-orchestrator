package benchv2

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func WriteReports(prefix string, aggregate Aggregate, results []TrialResult) error {
	rawJSON, err := json.MarshalIndent(aggregate, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(prefix+".json", append(rawJSON, '\n'), 0o644); err != nil {
		return err
	}
	file, err := os.Create(prefix + ".csv")
	if err != nil {
		return err
	}
	w := csv.NewWriter(file)
	_ = w.Write([]string{"task_id", "status", "trials", "passes", "pass_rate", "ci95_low", "ci95_high", "average_score", "weight", "hard_gate"})
	for _, item := range aggregate.Cases {
		_ = w.Write([]string{item.TaskID, item.Status, strconv.Itoa(item.Trials), strconv.Itoa(item.Passes),
			fmt.Sprintf("%.6f", item.PassRate), fmt.Sprintf("%.6f", item.CI95Low), fmt.Sprintf("%.6f", item.CI95High),
			fmt.Sprintf("%.2f", item.AverageScore), strconv.Itoa(item.Weight), strconv.FormatBool(item.HardGate)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	markdown := fmt.Sprintf("# CanvasBench v2 result\n\n- Matrix: `%s`\n- Weighted score: **%.2f**\n- Qualification threshold: **%.2f**\n- Eligible: **%t**\n\n| Case | Status | Passes | Rate | 95%% CI | Score |\n| --- | --- | ---: | ---: | --- | ---: |\n",
		aggregate.MatrixID, aggregate.WeightedScore, aggregate.Threshold, aggregate.Eligible)
	for _, item := range aggregate.Cases {
		label := item.TaskID
		if item.HardGate {
			label += " (hard gate)"
		}
		markdown += fmt.Sprintf("| %s | %s | %d/%d | %.1f%% | %.1f–%.1f%% | %.1f |\n", label, item.Status,
			item.Passes, item.Trials, 100*item.PassRate, 100*item.CI95Low, 100*item.CI95High, item.AverageScore)
	}
	if len(aggregate.IneligibleReasons) > 0 {
		markdown += "\n## Ineligibility\n\n"
		for _, reason := range aggregate.IneligibleReasons {
			markdown += "- " + reason + "\n"
		}
	}
	return os.WriteFile(prefix+".md", []byte(markdown), 0o644)
}

func AppendJSONL(path string, result TrialResult) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(result)
}
