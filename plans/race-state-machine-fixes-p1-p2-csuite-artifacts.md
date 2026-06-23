# P1/P2 Fix Plan: C-Suite Inbox, Pagination, And Artifact Registry

Findings: `RACE-SM-029`, `RACE-SM-030`, `RACE-SM-031`, `RACE-SM-039`, `RACE-SM-040`.

## Implemented Batch

- `RACE-SM-029`: map concurrent inbox item move source-missing errors to `ErrInboxItemNotFound`, so HTTP returns `404` instead of `500`.
- `RACE-SM-030`: move inbox items with no-overwrite semantics and return a controlled conflict on destination collision.
- `RACE-SM-031`: make cursor pagination mirror `(CreatedAt DESC, ID DESC)` ordering in both disk-backed and GORM-backed stores.
- `RACE-SM-039`: make artifact registration idempotent under same-URI concurrency by recovering from unique-content conflicts with a reload/update path.
- `RACE-SM-040`: guard supersede with `superseded_by_id IS NULL` so racing superseders produce one winner and one conflict.

## Acceptance Tests

- `internal/csuite/diskstore/diskstore_test.go`: destination collision returns conflict and same-timestamp pagination visits all IDs.
- `internal/serve/inbox_test.go`: inbox conflict maps to HTTP `409`.
- `internal/artifactregistry/registry_test.go`: same-URI concurrent registration succeeds with one row; concurrent supersede to different replacements yields one conflict.

## Target Files

- `internal/csuite/inbox_queue.go`
- `internal/csuite/diskstore/diskstore.go`
- `internal/csuite/diskstore/diskstore_test.go`
- `internal/csuite/store.go`
- `internal/serve/inbox.go`
- `internal/serve/inbox_test.go`
- `internal/artifactregistry/registry.go`
- `internal/artifactregistry/registry_test.go`
