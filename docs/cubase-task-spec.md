# Cubase observation task specifications

`dremctl create --spec` is the only supported path for turning a Cubase
Computer Use observation into an orchestrated Canvas task. The JSON can be an
inline object or a local file:

```sh
dremctl --actor codex:canvas-discovery create --spec observation.json
```

The actor in the JSON must match `--actor` (or `DREM_ACTOR`). The
`idempotency_key` identifies one submission. Reusing it with identical JSON
returns the original task; reusing it with different JSON returns a conflict.
An equivalent behavioral contract also resolves to an existing active task,
even when it came from a different capture session.

The media remains in the local artifact store. The specification contains
only stable artifact IDs, SHA-256 content hashes, media types, and textual
purposes. Do not put filesystem paths, URLs with credentials, or media bodies
in this JSON.

```json
{
  "title": "Match Cubase range comp selection",
  "description": "Canvas should reproduce the observed range comp gesture.",
  "actor": "codex:canvas-discovery",
  "idempotency_key": "cubase-range-comp-20260722-1",
  "observation": {
    "session_id": "cubase-session-20260722-a",
    "product": "Cubase Pro",
    "product_version": "15.0.10",
    "os": "Windows 11",
    "display_environment": "1920x1080@100%",
    "observed_at": "2026-07-22T19:00:00Z",
    "observer_actor": "codex:computer-use:canvas-discovery",
    "preconditions": [
      "An audio track has two overlapping takes."
    ],
    "steps": [
      {
        "action": "Drag across the lower take",
        "target": "take lane 2",
        "expected_visible_result": "The dragged range becomes the active comp segment."
      }
    ],
    "expected_behavior": [
      "The selected range is promoted without moving the event."
    ],
    "negative_behavior": [
      "The complete take is not promoted."
    ],
    "evidence": [
      {
        "artifact_id": "cubase-range-comp-before-after",
        "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "media_type": "image/png",
        "purpose": "Shows the gesture result."
      }
    ]
  },
  "acceptance_criteria": [
    {
      "id": "range-promotes-segment",
      "description": "A drag range promotes only the matching take segment.",
      "verification_steps": [
        "Create two takes.",
        "Drag across half of take lane 2."
      ],
      "expected_behavior": [
        "Only the dragged half uses take lane 2."
      ],
      "negative_behavior": [
        "The other half remains unchanged."
      ]
    }
  ],
  "proposed_scope": [
    "arrangement comp interaction",
    "take comp model"
  ],
  "exclusions": [
    "audio rendering",
    "session persistence"
  ],
  "dependencies": [],
  "uncertainty": [],
  "open_questions": []
}
```

All required lists must contain non-empty entries. Evidence accepts `image/*`,
`video/*`, `text/*`, or `application/json` media types and a 64-character
hexadecimal SHA-256. Image and video remain the normal evidence for Cubase
observations; text and JSON admit content-addressed repository, build, and
workflow evidence without pretending it is visual media. Executable and
arbitrary binary media remain rejected. Every acceptance criterion has a
unique ID, concrete verification steps, and an expected result. Non-empty
`open_questions` create the task in `needs_clarification`; otherwise it starts
in `classifying`.

The orchestrator stores the normalized immutable specification and each
criterion in typed records. It also emits an admitted text-only rendering into
the task description so downstream planning receives the workflow, criterion
IDs, evidence references, proposed scope, and exclusions without receiving the
media itself.

## Source-backed production seams

An adapter-authored `execution_plan` must also provide `integration_seams`.
Each seam maps acceptance-criterion IDs to a real production entrypoint,
includes an exact source excerpt plus the SHA-256 of those excerpt bytes,
declares the missing call, registration, or manifest edges, and names every
file needed to close them. Required edge files must appear in both
`proposed_scope` and the plan’s integration subtask. `verification_level` is
one of `automated_integration`, `native_runtime`, or `computer_use`, and its
steps must exercise the production entrypoint rather than merely searching
source text or calling an isolated helper.

This contract deliberately permits source evidence files outside the mutation
scope: an existing registrar or caller is often the evidence that reveals a
missing edge. It does not permit the edge’s writable file to remain excluded.
The plan reviewer receives the verified excerpts and must reject any additional
caller, registration, or manifest gap visible in that evidence.

## Planned interfaces for red tests

Every implementation subtask in an adapter-authored `execution_plan` supplies
`module_boundaries` plus either legacy exact `interface_shapes` or typed
`interface_contracts`; every test subtask points to one implementation through
`tests_for`. New Canvas specs should use typed contracts. The orchestrator
materializes them into both paired worker prompts together with the immutable,
hash-verified source excerpts and paired file list.

Each `interface_contracts` entry requires `package`, `kind`, `state`, and
`owner_file`. Supported kinds and their additional fields are:

| Kind | Additional fields |
|------|-------------------|
| `cpp_function` | `signature` |
| `cpp_type` | `symbol` |
| `registry_action` | `action_id`, `callback_signature` |
| `keymap_route` | `route`, `target_action` |
| `call_edge` | `caller`, `callee` |

`state` is `planned`, `existing`, or `missing`. Existing contracts require a
matching source-evidence path and symbol/excerpt. Missing contracts require a
matching `missing_edge`; planned contracts name new production API. Only
planned/missing C++ functions and types may intentionally compile-red. Runtime
registry, keymap, and call-edge tests must compile and fail on an active
behavioral assertion. Action IDs such as `audio.divide-transients` are never
callable signatures.

Legacy `interface_shapes` remain accepted for existing clients, but their
function entries must be real C++ signatures; dotted action IDs are rejected.
Vague behavioral labels belong in the subtask description and acceptance
criteria, not in either interface contract.
