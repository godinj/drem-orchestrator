// Package capability defines model capability types, a known-model registry,
// and per-agent-type capability requirements.
package capability

import (
	"sort"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Capability represents a model feature required by an agent type.
type Capability string

const (
	ToolCalling      Capability = "tool_calling"
	ExtendedThinking Capability = "extended_thinking"
	CodeExecution    Capability = "code_execution"
)

// CapabilitySet is a set of capabilities a model supports.
type CapabilitySet map[Capability]bool

// Has reports whether the set contains the given capability.
func (s CapabilitySet) Has(c Capability) bool { return s[c] }

// modelEntry pairs a prefix with its capability set for sorted lookup.
type modelEntry struct {
	prefix string
	caps   CapabilitySet
}

// knownModels is sorted longest-prefix-first for correct matching.
var knownModels []modelEntry

func init() {
	entries := []modelEntry{
		{"claude-opus-4", CapabilitySet{ToolCalling: true, ExtendedThinking: true, CodeExecution: true}},
		{"claude-sonnet-4", CapabilitySet{ToolCalling: true, ExtendedThinking: true, CodeExecution: true}},
		{"claude-haiku-4", CapabilitySet{ToolCalling: true, ExtendedThinking: true, CodeExecution: true}},
		{"claude-3-5-sonnet", CapabilitySet{ToolCalling: true, ExtendedThinking: true}},
		{"claude-3-5-haiku", CapabilitySet{ToolCalling: true}},
		{"claude-3-haiku", CapabilitySet{ToolCalling: true}},
	}
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].prefix) > len(entries[j].prefix)
	})
	knownModels = entries
}

// LookupModel returns the capability set for a model ID by longest-prefix
// match. The second return value is false if no known prefix matches.
func LookupModel(modelID string) (CapabilitySet, bool) {
	for _, e := range knownModels {
		if strings.HasPrefix(modelID, e.prefix) {
			return e.caps, true
		}
	}
	return nil, false
}

// agentRequirements maps each agent type to its required capabilities.
var agentRequirements = map[model.AgentType]CapabilitySet{
	model.AgentClassifier: {ToolCalling: true},
	model.AgentPlanner:    {ToolCalling: true, ExtendedThinking: true},
	model.AgentCoder:      {ToolCalling: true, ExtendedThinking: true},
	model.AgentReviewer:   {ToolCalling: true},
	model.AgentFixer:      {ToolCalling: true},
	model.AgentResearcher: {ToolCalling: true},
}

// RequirementsFor returns the required capabilities for the given agent type.
// Returns an empty set if the agent type has no defined requirements.
func RequirementsFor(agentType model.AgentType) CapabilitySet {
	if cs, ok := agentRequirements[agentType]; ok {
		return cs
	}
	return CapabilitySet{}
}
