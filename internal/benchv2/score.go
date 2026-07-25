package benchv2

import "math"

func Score(task TaskSpec, result *TrialResult) float64 {
	if result.Status == "non_runnable" {
		return 0
	}
	score := 0.0
	if result.Gates.VerifierPassed && result.Gates.Compiled {
		score += 55
	}
	if result.Gates.ScopePassed && result.Gates.ReadScopePassed {
		score += 15
	}
	if result.Gates.OracleIsolated {
		score += 10
	}
	if result.Telemetry.TokensIn <= task.Budget.MaxInputTokens && result.Telemetry.TokensOut <= task.Budget.MaxOutputTokens {
		score += 10
	}
	if result.Gates.Attested && result.Telemetry.PeakRequestInput > 0 {
		score += 10
	}
	if !result.Gates.Compiled || !result.Gates.ScopePassed || !result.Gates.ReadScopePassed ||
		!result.Gates.OracleIsolated || !result.Gates.Attested || !result.Gates.RequiredMutationPassed {
		return math.Min(score, 40)
	}
	return score
}

type CaseAggregate struct {
	TaskID       string  `json:"task_id"`
	Status       string  `json:"status"`
	Trials       int     `json:"trials"`
	Passes       int     `json:"passes"`
	PassRate     float64 `json:"pass_rate"`
	CI95Low      float64 `json:"ci95_low"`
	CI95High     float64 `json:"ci95_high"`
	AverageScore float64 `json:"average_score"`
	Weight       int     `json:"weight"`
}

type Aggregate struct {
	Schema            string          `json:"schema"`
	MatrixID          string          `json:"matrix_id"`
	Cases             []CaseAggregate `json:"cases"`
	WeightedScore     float64         `json:"weighted_score"`
	Threshold         float64         `json:"threshold"`
	Eligible          bool            `json:"eligible"`
	IneligibleReasons []string        `json:"ineligible_reasons"`
}

func AggregateResults(matrixID string, tasks []TaskSpec, results []TrialResult) Aggregate {
	aggregate := Aggregate{Schema: "canvasbench.aggregate.v2", MatrixID: matrixID, Threshold: 90, Eligible: true}
	byTask := map[string][]TrialResult{}
	for _, result := range results {
		byTask[result.TaskID] = append(byTask[result.TaskID], result)
	}
	weighted, weights := 0.0, 0
	for _, task := range tasks {
		caseResult := CaseAggregate{TaskID: task.ID, Status: task.Status, Weight: task.Weight}
		trials := byTask[task.ID]
		if task.Status != "runnable" {
			caseResult.Status = "non_runnable"
			aggregate.IneligibleReasons = append(aggregate.IneligibleReasons, task.ID+": hidden canonical artifact missing")
			aggregate.Cases = append(aggregate.Cases, caseResult)
			continue
		}
		caseResult.Trials = len(trials)
		for _, trial := range trials {
			caseResult.AverageScore += trial.Score
			if trial.Status == "passed" {
				caseResult.Passes++
			}
		}
		if len(trials) > 0 {
			caseResult.AverageScore /= float64(len(trials))
			caseResult.PassRate = float64(caseResult.Passes) / float64(len(trials))
			caseResult.CI95Low, caseResult.CI95High = wilson95(caseResult.Passes, len(trials))
		}
		weighted += caseResult.AverageScore * float64(task.Weight)
		weights += task.Weight
		if (task.ID == "case-08" || task.ID == "case-09") && caseResult.Passes != caseResult.Trials {
			aggregate.IneligibleReasons = append(aggregate.IneligibleReasons, task.ID+": mandatory case did not pass every trial")
		}
		aggregate.Cases = append(aggregate.Cases, caseResult)
	}
	if weights > 0 {
		aggregate.WeightedScore = weighted / float64(weights)
	}
	if aggregate.WeightedScore < aggregate.Threshold {
		aggregate.IneligibleReasons = append(aggregate.IneligibleReasons, "weighted suite score below 90")
	}
	aggregate.Eligible = len(aggregate.IneligibleReasons) == 0
	return aggregate
}

func wilson95(successes, total int) (float64, float64) {
	if total == 0 {
		return 0, 0
	}
	z := 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}
