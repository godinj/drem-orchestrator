package orchhttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

func validTaskSpec() orchdto.TaskSpecDTO {
	return orchdto.TaskSpecDTO{
		Title:          "Match Cubase range comp selection",
		Description:    "Canvas should reproduce the observed range comp gesture.",
		Actor:          "codex:test",
		IdempotencyKey: "cubase-range-comp-20260722-1",
		Observation: &orchdto.ReferenceObservationDTO{
			SessionID:          "cubase-session-1",
			Product:            "Cubase Pro",
			ProductVersion:     "15.0.10",
			OS:                 "Windows 11",
			DisplayEnvironment: "1920x1080@100%",
			ObservedAt:         time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
			ObserverActor:      "codex:computer-use:test",
			Preconditions:      []string{"An audio track has two overlapping takes."},
			Steps: []orchdto.ReferenceWorkflowStepDTO{{
				Action:                "Drag across the lower take",
				Target:                "take lane 2",
				ExpectedVisibleResult: "The dragged range becomes the active comp segment.",
			}},
			ExpectedBehavior: []string{"The selected range is promoted without moving the event."},
			NegativeBehavior: []string{"The complete take is not promoted."},
			Evidence: []orchdto.ObservationEvidenceDTO{{
				ArtifactID: "cubase-range-comp-before-after",
				SHA256:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				MediaType:  "image/png",
				Purpose:    "Shows the gesture result.",
			}},
		},
		AcceptanceCriteria: []orchdto.TaskAcceptanceCriterionDTO{{
			ID:                "range-promotes-segment",
			Description:       "A drag range promotes only the matching take segment.",
			VerificationSteps: []string{"Create two takes.", "Drag across half of take lane 2."},
			ExpectedBehavior:  []string{"Only the dragged half uses take lane 2."},
			NegativeBehavior:  []string{"The other half remains unchanged."},
		}},
		ProposedScope: []string{"arrangement comp interaction", "take comp model"},
		Exclusions:    []string{"audio rendering", "session persistence"},
	}
}

func postTaskSpec(t *testing.T, baseURL string, spec orchdto.TaskSpecDTO) *http.Response {
	t.Helper()
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/projects/"+projectName+"/tasks", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Drem-Actor", "codex:test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestCreateTaskSpecPersistsTypedObservationAndCriteria(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	resp := postTaskSpec(t, ts.URL, spec)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	require.Equal(t, string(model.StatusClassifying), task.Status)

	var stored model.TaskSpecification
	require.NoError(t, srv.DB.First(&stored, "task_id = ?", task.ID).Error)
	require.Equal(t, spec.Observation.SessionID, stored.ObservationSessionID)
	require.Equal(t, "codex:test", stored.CreatorActor)
	require.Len(t, stored.RequestHash, 64)
	require.NotContains(t, stored.SpecJSON, "/Users/")

	var criteria []model.TaskAcceptanceCriterion
	require.NoError(t, srv.DB.Where("task_id = ?", task.ID).Order("position").Find(&criteria).Error)
	require.Len(t, criteria, 1)
	require.Equal(t, "range-promotes-segment", criteria[0].CriterionKey)

	var taskRow model.Task
	require.NoError(t, srv.DB.First(&taskRow, "id = ?", task.ID).Error)
	require.Contains(t, taskRow.Description, "Evidence references:")
	require.Contains(t, taskRow.Description, "sha256:")
}

func TestCreateTaskSpecIdempotentReplayReturnsOriginalTask(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	first := postTaskSpec(t, ts.URL, spec)
	defer first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)
	var created orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(first.Body).Decode(&created))

	replay := postTaskSpec(t, ts.URL, spec)
	defer replay.Body.Close()
	require.Equal(t, http.StatusOK, replay.StatusCode)
	require.Equal(t, "true", replay.Header.Get("X-Drem-Idempotent-Replay"))
	var got orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(replay.Body).Decode(&got))
	require.Equal(t, created.ID, got.ID)

	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskSpecification{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCreateTaskSpecConcurrentReplayCreatesOneTask(t *testing.T) {
	srv, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	const callers = 8
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/projects/"+projectName+"/tasks", bytes.NewReader(raw))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer secret-token")
			req.Header.Set("X-Drem-Actor", "codex:test")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			var got orchdto.TaskDTO
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				errs <- err
				return
			}
			ids <- got.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	unique := map[string]struct{}{}
	for id := range ids {
		unique[id] = struct{}{}
	}
	require.Len(t, unique, 1)
	var count int64
	require.NoError(t, srv.DB.Model(&model.TaskSpecification{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCreateTaskSpecRejectsReusedIdempotencyKeyWithDifferentBody(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	first := postTaskSpec(t, ts.URL, spec)
	first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)

	spec.Description = "A materially different task."
	conflict := postTaskSpec(t, ts.URL, spec)
	defer conflict.Body.Close()
	require.Equal(t, http.StatusConflict, conflict.StatusCode)
}

func TestCreateTaskSpecDeduplicatesEquivalentActiveObservation(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	first := postTaskSpec(t, ts.URL, spec)
	defer first.Body.Close()
	var created orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(first.Body).Decode(&created))

	spec.IdempotencyKey = "cubase-range-comp-20260722-2"
	spec.Observation.SessionID = "cubase-session-2"
	spec.Observation.ObservedAt = spec.Observation.ObservedAt.Add(time.Hour)
	spec.Observation.Evidence[0].ArtifactID = "cubase-range-comp-second-capture"
	duplicate := postTaskSpec(t, ts.URL, spec)
	defer duplicate.Body.Close()
	require.Equal(t, http.StatusOK, duplicate.StatusCode)
	require.Equal(t, "true", duplicate.Header.Get("X-Drem-Deduplicated"))
	var got orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(duplicate.Body).Decode(&got))
	require.Equal(t, created.ID, got.ID)
}

func TestCreateTaskSpecWithOpenQuestionsWaitsForClarification(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	spec.OpenQuestions = []string{"Does Cubase apply snap before or after lane selection?"}
	resp := postTaskSpec(t, ts.URL, spec)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var got orchdto.TaskDTO
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, string(model.StatusNeedsClarification), got.Status)
}

func TestCreateTaskSpecRejectsBadEvidenceAndActorMismatch(t *testing.T) {
	_, ts, _ := setupHTTPTest(t, nil)
	spec := validTaskSpec()
	spec.Observation.Evidence[0].SHA256 = "not-a-digest"
	resp := postTaskSpec(t, ts.URL, spec)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	spec = validTaskSpec()
	spec.Actor = "codex:different"
	resp = postTaskSpec(t, ts.URL, spec)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
