# Test Specification: TUI Agent Panel ModelID and Cost Display

## Overview

This specification defines comprehensive test coverage for displaying agent enrichment fields (ModelID and TotalCostUSD) in the TUI agent panel. Tests are written using TDD approach and currently FAIL — they define the expected behavior before implementation.

## Test Files

### 1. `internal/tui/agents_display_helpers_test.go`
Tests for helper functions that extract and format fields from agent.Config.

#### Tests: TestExtractModelID
Tests the `extractModelID(agent *model.Agent) string` function:

- **nil agent** → returns "-"
- **nil Config** → returns "-"
- **empty Config** → returns "-"
- **missing model_id key** → returns "-"
- **empty string model_id** → returns "-"
- **valid model_id** → returns the value as-is (e.g., "claude-opus-4")
- **model_id with special characters** → preserves full value
- **wrong type (non-string)** → returns "-"
- **model_id with whitespace** → preserves whitespace

**Status**: ✅ PASSING - Helper function correctly extracts and handles edge cases

#### Tests: TestExtractCost
Tests the `extractCost(agent *model.Agent) string` function:

- **nil agent** → returns "-"
- **nil Config** → returns "-"
- **empty Config** → returns "-"
- **missing total_cost_usd key** → returns "-"
- **zero cost (0.0)** → returns "$0.00" (not "-")
- **float64 with two decimals** (1.50) → "$1.50"
- **float64 with one decimal** (5.5) → "$5.50" (padded)
- **float64 with three decimals** (1.234) → "$1.23" (rounded down)
- **float64 with three decimals** (1.235) → "$1.24" (rounded up)
- **large cost values** (123.45) → "$123.45"
- **float32 type** → converted and formatted correctly
- **int type** → converted and formatted as "$N.00"
- **int64 type** → converted and formatted correctly
- **string type (wrong)** → returns "-"
- **very small positive cost** (0.01) → "$0.01"
- **many decimal places** (0.123456789) → "$0.12"
- **nil value for cost field** → returns "-"

**Status**: ✅ PASSING - Helper function correctly formats costs with proper rounding

### 2. `internal/tui/agents_model_display_test.go`
Tests for AgentsModel.View() displaying ModelID and Cost columns.

#### Tests: TestAgentsViewDisplay_ModelIDAndCostColumns
Tests that View() renders both ModelID and Cost columns for various scenarios:

- **agent with valid model_id and cost** → displays "claude-opus-4" and "$1.50"
- **empty model_id** → shows "-" for model_id, displays cost "$0.42"
- **no model_id key in Config** → shows "-", displays cost "$2.15"
- **zero cost** → displays model_id "claude-haiku" and "$0.00"
- **no total_cost_usd key** → displays model_id "claude-sonnet" and "-"
- **nil Config** → displays "-" for both
- **empty Config map** → displays "-" for both
- **rounding behavior** → formats cost properly (rounds to 2 decimals)
- **large costs** → displays "claude-opus-4" and "$123.45"
- **long model_id values** → may truncate based on terminal width

**Status**: ❌ FAILING - View() does not yet display columns

#### Tests: TestAgentsViewDisplay_ModelIDExtractionFromConfig
Tests proper extraction of model_id field values:

- **string type model_id** → displays correctly
- **empty string** → displays "-"
- **missing key** → displays "-"

**Status**: ⚠️ PARTIALLY PASSING (dash cases pass, actual values fail)

#### Tests: TestAgentsViewDisplay_CostFormattingEdgeCases
Tests cost formatting edge cases in rendered output:

- **zero cost** → "$0.00"
- **single decimal** (5.5) → "$5.50"
- **two decimals** (10.99) → "$10.99"
- **three decimals (down)** (1.234) → "$1.23"
- **three decimals (up)** (1.235) → "$1.24"
- **many decimals** (0.123456789) → "$0.12"
- **missing cost key** → "-"

**Status**: ❌ FAILING - View() does not format costs

#### Tests: TestAgentsViewDisplay_MultipleAgentsWithDifferentValues
Tests that multiple agents with varied configurations all display correctly:

- agent-1: model_id "claude-opus-4", cost $5.25
- agent-2: model_id "claude-haiku", cost $0.05
- agent-3: no model_id, cost $1.11
- agent-4: model_id "claude-sonnet", no cost

All four agents should show their respective values with proper handling of missing fields.

**Status**: ❌ FAILING - View() does not display any columns

#### Tests: TestAgentsViewDisplay_ModelIDAndCostNotDisplayedForArchivedAgents
Tests behavior of showing/hiding ModelID and Cost for archived agents:

- **dead agent with showArchived=true** → displays model_id and cost
- **dead agent with showArchived=false** → agent not visible (values not shown)

**Status**: ⚠️ PARTIALLY PASSING (hidden agents pass, displayed values fail)

#### Tests: TestAgentsViewDisplay_ColumnFormatting
Tests consistent column formatting across multiple agents:

- short agent name and long agent name should both display their model_id and cost
- column positions should be consistent

**Status**: ❌ FAILING - Columns not displayed

## Helper Functions (Stubs)

### extractModelID(agent *model.Agent) string
**Location**: `internal/tui/agents_display_helpers.go`

Extracts the `model_id` field from agent.Config:
- Returns the string value if present and non-empty
- Returns "-" for nil agent, nil Config, missing key, or empty string
- Handles type assertion gracefully

### extractCost(agent *model.Agent) string
**Location**: `internal/tui/agents_display_helpers.go`

Extracts and formats the `total_cost_usd` field from agent.Config:
- Returns "-" for nil agent, nil Config, or missing key
- Handles multiple numeric types (float64, float32, int, int64)
- Formats as "$X.XX" with proper rounding (banker's rounding via fmt.Sprintf)
- Returns "$0.00" for zero cost (not "-")
- Returns "-" for non-numeric types

## Implementation Checklist

To make all tests pass, AgentsModel.View() must:

1. **Extract fields** using `extractModelID()` and `extractCost()` helpers
2. **Add display columns** to the agent header/detail lines
   - ModelID column after status badge
   - Cost column after ModelID
3. **Handle width constraints**
   - Truncate long model_ids based on available width
   - Ensure columns don't overflow on standard terminal widths (120+ columns)
4. **Maintain formatting**
   - Model IDs display as-is or "-"
   - Costs display as "$X.XX" or "-"
5. **Respect visibility settings**
   - Show values for all visible agents
   - Respect showArchived toggle (values shown when agent is visible)
   - Respect task filter (values shown for filtered agents)

## Column Layout Examples

### Current (without ModelID/Cost)
```
> agent-1   working
    session: tmux-1
    branch: feature/xyz
```

### Expected (with ModelID/Cost)
```
> agent-1   working   claude-opus-4   $5.25
    session: tmux-1
    branch: feature/xyz
```

Or integrated into the header line, depending on implementation choice:
```
> agent-1   working [claude-opus-4] [$5.25]
    session: tmux-1
    branch: feature/xyz
```

## Test Statistics

- **Total test functions**: 7
- **Total test cases**: 60+
- **Helper function tests**: 27 (passing)
- **Display tests**: 33+ (mostly failing - expected for TDD)
- **Passing tests**: 32 (helper tests + edge cases for nil/empty configs)
- **Failing tests**: 28 (awaiting implementation)

## Notes for Implementation

1. The helper functions are ready to use - they're properly tested and handle all edge cases
2. Tests use standard Go testing patterns and TUI test patterns from existing code
3. Tests are isolated and don't require database or real agents - they work with in-memory model.Agent objects
4. The task requirement specifies zero cost should show "$0.00" (not "-"), which is implemented in helpers
5. Tests check for values appearing anywhere in output (using strings.Contains), allowing flexibility in exact column placement
