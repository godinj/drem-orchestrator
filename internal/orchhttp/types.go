package orchhttp

import "github.com/godinj/drem-orchestrator/pkg/orchdto"

// The public DTO shapes live in pkg/orchdto so Kyle and the TUI can
// import them without pulling internal/. The aliases below let handler
// code in this package refer to them by short names. Keeping the
// aliases in a separate file (rather than scattering the orchdto import
// across every handler file) makes it clear where the schema boundary
// lives.

// ProjectDTO is the public project descriptor. Exported here as an
// alias to keep handler call-sites readable.
type ProjectDTO = orchdto.ProjectDTO

// TaskDTO is the public task projection.
type TaskDTO = orchdto.TaskDTO

// WorkerDTO is the public worker projection.
type WorkerDTO = orchdto.WorkerDTO

// WorkerHistoryDTO is the public worker-history response shape.
type WorkerHistoryDTO = orchdto.WorkerHistoryDTO

// WorkerHistoryEntry is one row of a WorkerHistoryDTO.
type WorkerHistoryEntry = orchdto.WorkerHistoryEntry

// EventDTO is the public event shape.
type EventDTO = orchdto.EventDTO

// IngestRequest is the shape decoded by POST /internal/logs.
type IngestRequest = orchdto.IngestRequest

// IngestResponse is the shape returned by POST /internal/logs on
// success.
type IngestResponse = orchdto.IngestResponse
