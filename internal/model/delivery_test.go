package model

import "testing"

func TestParseDeliveryPolicies(t *testing.T) {
	if got, err := ParseIntegrationPolicy("prepare_branch"); err != nil || got != IntegrationPrepareBranch {
		t.Fatalf("ParseIntegrationPolicy: got %q, err %v", got, err)
	}
	if got, err := ParseVerificationPolicy("external_ack"); err != nil || got != VerificationExternalAck {
		t.Fatalf("ParseVerificationPolicy: got %q, err %v", got, err)
	}
	if _, err := ParseIntegrationPolicy(""); err == nil {
		t.Fatal("empty integration policy must fail closed")
	}
	if _, err := ParseVerificationPolicy("native-ish"); err == nil {
		t.Fatal("unknown verification policy must fail closed")
	}
}

func TestParseVerificationResult(t *testing.T) {
	for _, raw := range []string{"pass", "fail"} {
		if _, err := ParseVerificationResult(raw); err != nil {
			t.Fatalf("ParseVerificationResult(%q): %v", raw, err)
		}
	}
	if _, err := ParseVerificationResult("maybe"); err == nil {
		t.Fatal("unknown verification result must be rejected")
	}
}

func TestParsePreliminaryGateOutcome(t *testing.T) {
	for _, raw := range []string{"pass", "code", "infra", "configuration", "timeout", "cancelled"} {
		if _, err := ParsePreliminaryGateOutcome(raw); err != nil {
			t.Fatalf("ParsePreliminaryGateOutcome(%q): %v", raw, err)
		}
	}
	if _, err := ParsePreliminaryGateOutcome("unknown"); err == nil {
		t.Fatal("unknown preliminary gate outcome must be rejected")
	}
}
