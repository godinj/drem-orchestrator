package images

import (
	"testing"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// intentionallyUnmapped is the allowlist of AgentType constants that
// deliberately lack an image mapping in DefaultImages. These are not
// spawn-on-demand roles and must never gain an image here without a
// corresponding architectural change.
//
// - AgentPlanner: warm drem-planner lives in deploy/compose/global.yml
//   and the orchestrator reaches it over HTTP via dispatchPlanHTTP.
//   See plans/warm-planner-pivot.md §7.
// - AgentOrchestrator: the orchestrator itself is the process that
//   spawns the others — it cannot be a spawn target.
// - AgentResearcher: no container image exists for the researcher role
//   yet; keeping it unmapped surfaces a clean error at dispatch time
//   rather than silently routing to a stale image tag.
// - AgentPrep: warm direct-prep path in internal/agent/direct_prep.go;
//   no spawn-on-demand container. See plans/warm-direct-prep.md.
var intentionallyUnmapped = map[model.AgentType]struct{}{
	model.AgentOrchestrator: {},
	model.AgentPlanner:      {},
	model.AgentResearcher:   {},
	model.AgentPrep:         {},
}

// TestEnumCoverage_EveryAgentTypeResolvesOrIsAllowlisted is the permanent
// drift guard per plans/bug-f-spawner-image-registry-drift.md §Regression
// proof. It enumerates every AgentType constant declared in
// internal/model/enums.go and asserts one of:
//
//  1. The type resolves to an image via Resolve (directly, or via the
//     coder-language synthesis for AgentCoder with lang=go).
//  2. The type appears in intentionallyUnmapped.
//
// A new AgentType constant that forgets to add an image mapping here
// (or to update the allowlist with a justification) fails this test.
// Adding a new constant to internal/model/enums.go without updating
// this file becomes compile-visible drift — the whole point of the
// Bug F refactor.
func TestEnumCoverage_EveryAgentTypeResolvesOrIsAllowlisted(t *testing.T) {
	// Exhaustive list of AgentType constants. Mirrored by hand from
	// internal/model/enums.go; if a new constant is added there, this
	// list must grow too. The test catches that omission on the next
	// CI run.
	agentTypes := []model.AgentType{
		model.AgentOrchestrator,
		model.AgentPlanner,
		model.AgentCoder,
		model.AgentResearcher,
		model.AgentReviewer,
		model.AgentFixer,
		model.AgentClassifier,
		model.AgentPrep,
	}

	for _, at := range agentTypes {
		at := at
		t.Run(string(at), func(t *testing.T) {
			// Coder is language-sensitive. Feed it a valid language
			// ("go") and expect a mapping — the enum coverage check
			// does not care whether the spawner also knows about "cpp",
			// only that the AgentCoder constant has some image.
			labels := map[string]string(nil)
			if at == model.AgentCoder {
				labels = map[string]string{"drem.language": "go"}
			}
			_, ok := Resolve(string(at), labels)
			if ok {
				return
			}
			if _, allow := intentionallyUnmapped[at]; allow {
				return
			}
			t.Fatalf("AgentType %q has no image mapping and is not in intentionallyUnmapped — add it to DefaultImages or justify the omission in the allowlist", at)
		})
	}
}

// TestEnumCoverage_AllowlistAgentTypesAreReal makes sure the
// intentionallyUnmapped allowlist itself does not rot: every key must
// be a real AgentType constant (detected via ParseAgentType). Prevents
// typos in the allowlist from silently masking a real mapping gap.
func TestEnumCoverage_AllowlistAgentTypesAreReal(t *testing.T) {
	for at := range intentionallyUnmapped {
		if _, err := model.ParseAgentType(string(at)); err != nil {
			t.Fatalf("intentionallyUnmapped key %q is not a valid AgentType: %v", at, err)
		}
	}
}
