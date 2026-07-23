package model

import "fmt"

// PreliminaryGateOutcome is the closed result/failure classification for an
// isolated preliminary gate run.
type PreliminaryGateOutcome string

const (
	PreliminaryGatePassed        PreliminaryGateOutcome = "pass"
	PreliminaryGateCodeFailure   PreliminaryGateOutcome = "code"
	PreliminaryGateInfraFailure  PreliminaryGateOutcome = "infra"
	PreliminaryGateConfiguration PreliminaryGateOutcome = "configuration"
	PreliminaryGateTimeout       PreliminaryGateOutcome = "timeout"
	PreliminaryGateCancelled     PreliminaryGateOutcome = "cancelled"
)

func ParsePreliminaryGateOutcome(raw string) (PreliminaryGateOutcome, error) {
	switch PreliminaryGateOutcome(raw) {
	case PreliminaryGatePassed, PreliminaryGateCodeFailure, PreliminaryGateInfraFailure,
		PreliminaryGateConfiguration, PreliminaryGateTimeout, PreliminaryGateCancelled:
		return PreliminaryGateOutcome(raw), nil
	default:
		return "", fmt.Errorf("unknown preliminary gate outcome: %q", raw)
	}
}

// VerificationResult is the closed result set accepted by host and automated
// verifiers. Verification records are append-only, including failures.
type VerificationResult string

const (
	VerificationPassed VerificationResult = "pass"
	VerificationFailed VerificationResult = "fail"
)

func ParseVerificationResult(raw string) (VerificationResult, error) {
	switch VerificationResult(raw) {
	case VerificationPassed, VerificationFailed:
		return VerificationResult(raw), nil
	default:
		return "", fmt.Errorf("unknown verification result: %q", raw)
	}
}

// IntegrationPolicy controls whether a verified task waits for explicit
// authorization or advances automatically toward merging.
type IntegrationPolicy string

const (
	IntegrationPrepareBranch IntegrationPolicy = "prepare_branch"
	IntegrationAutoMerge     IntegrationPolicy = "auto_merge"
)

func ParseIntegrationPolicy(raw string) (IntegrationPolicy, error) {
	switch IntegrationPolicy(raw) {
	case IntegrationPrepareBranch, IntegrationAutoMerge:
		return IntegrationPolicy(raw), nil
	default:
		return "", fmt.Errorf("unknown integration policy: %q", raw)
	}
}

// VerificationPolicy controls where verification evidence is produced.
type VerificationPolicy string

const (
	VerificationExternalAck    VerificationPolicy = "external_ack"
	VerificationLocalAutomated VerificationPolicy = "local_automated"
)

func ParseVerificationPolicy(raw string) (VerificationPolicy, error) {
	switch VerificationPolicy(raw) {
	case VerificationExternalAck, VerificationLocalAutomated:
		return VerificationPolicy(raw), nil
	default:
		return "", fmt.Errorf("unknown verification policy: %q", raw)
	}
}

// ReviewGatePolicy controls whether an approval gate waits for an operator or
// may be advanced by the fail-closed SGLang reviewer. Automatic policy only
// advances explicit "approve" recommendations; every other result stays
// parked for a Codex/operator decision.
type ReviewGatePolicy string

const (
	ReviewGateManual         ReviewGatePolicy = "manual"
	ReviewGateSGLangSafeAuto ReviewGatePolicy = "sglang_safe_auto"
)

func ParseReviewGatePolicy(raw string) (ReviewGatePolicy, error) {
	switch ReviewGatePolicy(raw) {
	case ReviewGateManual, ReviewGateSGLangSafeAuto:
		return ReviewGatePolicy(raw), nil
	default:
		return "", fmt.Errorf("unknown review gate policy: %q", raw)
	}
}
