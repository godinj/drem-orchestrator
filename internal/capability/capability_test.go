package capability

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

func TestLookupModel_ExactPrefix(t *testing.T) {
	caps, ok := LookupModel("claude-sonnet-4-6-20250514")
	if !ok {
		t.Fatal("expected match for claude-sonnet-4-6-20250514")
	}
	if !caps.Has(ToolCalling) {
		t.Error("expected ToolCalling")
	}
	if !caps.Has(ExtendedThinking) {
		t.Error("expected ExtendedThinking")
	}
	if !caps.Has(CodeExecution) {
		t.Error("expected CodeExecution")
	}
}

func TestLookupModel_ShortPrefix(t *testing.T) {
	caps, ok := LookupModel("claude-opus-4")
	if !ok {
		t.Fatal("expected match for claude-opus-4")
	}
	if !caps.Has(CodeExecution) {
		t.Error("expected CodeExecution")
	}
}

func TestLookupModel_Unknown(t *testing.T) {
	_, ok := LookupModel("gpt-4o")
	if ok {
		t.Error("expected no match for gpt-4o")
	}
}

func TestLookupModel_OlderModel(t *testing.T) {
	caps, ok := LookupModel("claude-3-5-sonnet-20241022")
	if !ok {
		t.Fatal("expected match for claude-3-5-sonnet")
	}
	if !caps.Has(ExtendedThinking) {
		t.Error("expected ExtendedThinking for claude-3-5-sonnet")
	}
	if caps.Has(CodeExecution) {
		t.Error("claude-3-5-sonnet should not have CodeExecution")
	}
}

func TestLookupModel_Claude3Haiku(t *testing.T) {
	caps, ok := LookupModel("claude-3-haiku-20240307")
	if !ok {
		t.Fatal("expected match for claude-3-haiku")
	}
	if !caps.Has(ToolCalling) {
		t.Error("expected ToolCalling")
	}
	if caps.Has(ExtendedThinking) {
		t.Error("claude-3-haiku should not have ExtendedThinking")
	}
}

func TestLookupModel_PrefixOrdering(t *testing.T) {
	// claude-3-5-haiku should match claude-3-5-haiku (longer) not claude-3-haiku (shorter)
	caps, ok := LookupModel("claude-3-5-haiku-20241022")
	if !ok {
		t.Fatal("expected match for claude-3-5-haiku")
	}
	if caps.Has(ExtendedThinking) {
		t.Error("claude-3-5-haiku should not have ExtendedThinking")
	}
}

func TestRequirementsFor_Coder(t *testing.T) {
	reqs := RequirementsFor(model.AgentCoder)
	if !reqs.Has(ToolCalling) {
		t.Error("coder should require ToolCalling")
	}
	if !reqs.Has(ExtendedThinking) {
		t.Error("coder should require ExtendedThinking")
	}
}

func TestRequirementsFor_Reviewer(t *testing.T) {
	reqs := RequirementsFor(model.AgentReviewer)
	if !reqs.Has(ToolCalling) {
		t.Error("reviewer should require ToolCalling")
	}
	if reqs.Has(ExtendedThinking) {
		t.Error("reviewer should not require ExtendedThinking")
	}
}

func TestRequirementsFor_Unknown(t *testing.T) {
	reqs := RequirementsFor("nonexistent")
	if len(reqs) != 0 {
		t.Error("unknown agent type should return empty set")
	}
}
