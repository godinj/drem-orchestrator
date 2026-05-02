# StandardParentPipelineCanary Documentation

## Purpose

The `StandardParentPipelineCanary` is a specialized monitoring and validation mechanism designed to ensure the integrity of orchestration workflows. It acts as a "canary" within the parent pipeline to detect if subtasks (orchestration components) are being correctly registered, executed, and reported.

By injecting a known, predictable canary record into the pipeline, the system can verify that the orchestration engine is correctly capturing the full lifecycle of all sub-operations.

## Orchestration Subtask Visibility

A primary goal of this canary is to support **subtask visibility**. In complex orchestration scenarios, a parent task may spawn multiple subtasks. If the orchestration layer fails to properly track these subtasks, the parent task might appear successful even if critical sub-components failed or were skipped.

The `StandardParentPipelineCanary` ensures that:
1. The parent task's relationship with its subtasks is correctly mapped.
2. The state transitions of subtasks are visible to the parent.
3. No subtask is "lost" during the orchestration process.

## Field Definitions

The canary record contains several key fields used for validation:

| Field | Description |
| :--- | :--- |
| `canary_id` | A unique identifier for the canary instance, used to distinguish it from real workload tasks. |
| `parent_task_id` | The ID of the parent orchestration task that spawned the canary. |
| `expected_subtask_count` | The number of subtasks that *must* be present for the canary to be considered valid. |
| `canary_status` | The current lifecycle state of the canary (e.g., `PENDING`, `COMPLETED`, `FAILED`). |
| `validation_timestamp` | The time at which the canary's completeness was verified. |

## Validation Logic

Validation of the `StandardParentPipelineCanary` is performed by the orchestration validator. A canary record is considered **complete and valid** only if:

1. **Identity Match**: The `canary_id` is correctly identified within the task stream.
2. **Parent Linkage**: The `parent_task_id` matches the active orchestration context.
3. **Completeness Check**: The number of recorded subtasks associated with the `parent_task_id` matches or exceeds the `expected_subtask_count`.
4. **State Integrity**: The `canary_status` reflects a terminal state (e.g., `COMPLETED`) only after all expected subtasks have reached a terminal state.

If the `expected_subtask_count` is not met, the canary triggers a `SUBTASK_VISIBILITY_FAILURE`, alerting operators that the orchestration engine may be dropping subtask telemetry.
