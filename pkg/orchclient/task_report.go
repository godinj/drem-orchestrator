package orchclient

import (
	"context"
	"net/url"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

// TaskReport returns one correlated lifecycle and inference measurement
// snapshot for a parent task and all of its immediate child work.
func (c *Client) TaskReport(ctx context.Context, project string, taskID uuid.UUID) (orchdto.TaskReportDTO, error) {
	var out orchdto.TaskReportDTO
	path := "/projects/" + url.PathEscape(project) + "/tasks/" + url.PathEscape(taskID.String()) + "/report"
	if err := c.get(ctx, path, nil, &out); err != nil {
		return orchdto.TaskReportDTO{}, err
	}
	return out, nil
}
