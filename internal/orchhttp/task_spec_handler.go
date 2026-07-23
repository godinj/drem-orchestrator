package orchhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/model"
	"github.com/godinj/drem-orchestrator/pkg/orchdto"
)

var inactiveTaskStatuses = []model.TaskStatus{
	model.StatusDone,
	model.StatusFailed,
	model.StatusRejected,
	model.StatusCancelled,
}

var stableReferenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func isTaskSpecRequest(spec orchdto.TaskSpecDTO) bool {
	return spec.Observation != nil || strings.TrimSpace(spec.IdempotencyKey) != "" || len(spec.AcceptanceCriteria) > 0
}

func (s *Server) handleCreateTaskSpec(w http.ResponseWriter, r *http.Request, spec orchdto.TaskSpecDTO) {
	actor, ok := requireMutationActor(w, r)
	if !ok {
		return
	}
	normalizeTaskSpec(&spec)
	if err := validateTaskSpec(spec, actor); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	specJSON, requestHash, fingerprint, err := taskSpecHashes(spec)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode task specification")
		return
	}
	// One orchestrator process is the sole project-DB writer. Serialize this
	// check/create section so concurrent retries cannot both pass the replay
	// and active-dedup lookups before either immutable record exists.
	s.taskSpecMu.Lock()
	defer s.taskSpecMu.Unlock()

	var task model.Task
	status := http.StatusCreated
	replay := false
	deduplicated := false
	err = s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var project model.Project
		if err := tx.Where("name = ?", s.Project.Name).First(&project).Error; err != nil {
			return err
		}

		var existing model.TaskSpecification
		err := tx.Where("project_id = ? AND idempotency_key = ?", project.ID, spec.IdempotencyKey).Take(&existing).Error
		if err == nil {
			if existing.RequestHash != requestHash {
				return &taskSpecConflict{message: "idempotency key already used for a different task specification"}
			}
			if err := tx.Where("id = ?", existing.TaskID).Take(&task).Error; err != nil {
				return err
			}
			replay = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		err = tx.Model(&model.TaskSpecification{}).
			Joins("JOIN tasks ON tasks.id = task_specifications.task_id").
			Where("task_specifications.project_id = ? AND task_specifications.spec_fingerprint = ?", project.ID, fingerprint).
			Where("tasks.status NOT IN ?", inactiveTaskStatuses).
			Order("task_specifications.created_at DESC").
			Take(&existing).Error
		if err == nil {
			if err := tx.Where("id = ?", existing.TaskID).Take(&task).Error; err != nil {
				return err
			}
			deduplicated = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		taskStatus := model.StatusClassifying
		if len(spec.OpenQuestions) > 0 {
			taskStatus = model.StatusNeedsClarification
		} else if spec.ExecutionPlan != nil {
			taskStatus = model.StatusPlanReview
		}
		task = model.Task{
			ID:          uuid.New(),
			ProjectID:   project.ID,
			Title:       spec.Title,
			Description: renderTaskSpecDescription(spec),
			Status:      taskStatus,
			Category:    model.CategoryStandard,
		}
		if spec.ExecutionPlan != nil {
			plan, err := executionPlanField(spec.ExecutionPlan)
			if err != nil {
				return err
			}
			task.Plan = plan
		}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}

		now := time.Now()
		specification := model.TaskSpecification{
			ID:                   uuid.New(),
			TaskID:               task.ID,
			ProjectID:            project.ID,
			ObservationSessionID: spec.Observation.SessionID,
			Product:              spec.Observation.Product,
			ProductVersion:       spec.Observation.ProductVersion,
			OperatingSystem:      spec.Observation.OS,
			DisplayEnvironment:   spec.Observation.DisplayEnvironment,
			ObservedAt:           spec.Observation.ObservedAt,
			ObserverActor:        spec.Observation.ObserverActor,
			CreatorActor:         actor,
			IdempotencyKey:       spec.IdempotencyKey,
			RequestHash:          requestHash,
			SpecFingerprint:      fingerprint,
			SpecJSON:             string(specJSON),
			CreatedAt:            now,
		}
		if err := tx.Create(&specification).Error; err != nil {
			return err
		}
		for i, criterion := range spec.AcceptanceCriteria {
			steps, _ := json.Marshal(criterion.VerificationSteps)
			expected, _ := json.Marshal(criterion.ExpectedBehavior)
			negative, _ := json.Marshal(criterion.NegativeBehavior)
			row := model.TaskAcceptanceCriterion{
				ID:                    uuid.New(),
				SpecificationID:       specification.ID,
				TaskID:                task.ID,
				CriterionKey:          criterion.ID,
				Position:              i,
				Description:           criterion.Description,
				VerificationStepsJSON: string(steps),
				ExpectedBehaviorJSON:  string(expected),
				NegativeBehaviorJSON:  string(negative),
				CreatedAt:             now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		return tx.Create(&model.TaskEvent{
			ID:        uuid.New(),
			TaskID:    task.ID,
			EventType: "task_created_from_reference_observation",
			NewValue:  string(taskStatus),
			Details: model.JSONField{
				"specification_id":       specification.ID.String(),
				"observation_session_id": spec.Observation.SessionID,
				"evidence_count":         len(spec.Observation.Evidence),
				"acceptance_count":       len(spec.AcceptanceCriteria),
				"open_questions":         spec.OpenQuestions,
				"adapter_execution_plan": spec.ExecutionPlan != nil,
			},
			Actor:     actor,
			CreatedAt: now,
		}).Error
	})
	var conflict *taskSpecConflict
	if errors.As(err, &conflict) {
		writeJSONError(w, http.StatusConflict, conflict.Error())
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeJSONError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	if replay {
		status = http.StatusOK
		w.Header().Set("X-Drem-Idempotent-Replay", "true")
	}
	if deduplicated {
		status = http.StatusOK
		w.Header().Set("X-Drem-Deduplicated", "true")
	}
	writeJSON(w, status, toTaskDTO(task))
}

type taskSpecConflict struct{ message string }

func (e *taskSpecConflict) Error() string { return e.message }

func normalizeTaskSpec(spec *orchdto.TaskSpecDTO) {
	spec.Title = strings.TrimSpace(spec.Title)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Actor = strings.TrimSpace(spec.Actor)
	spec.IdempotencyKey = strings.TrimSpace(spec.IdempotencyKey)
	trimStrings(spec.ProposedScope)
	trimStrings(spec.Exclusions)
	trimStrings(spec.Dependencies)
	trimStrings(spec.Uncertainty)
	trimStrings(spec.OpenQuestions)
	if spec.Observation == nil {
		return
	}
	o := spec.Observation
	o.SessionID = strings.TrimSpace(o.SessionID)
	o.Product = strings.TrimSpace(o.Product)
	o.ProductVersion = strings.TrimSpace(o.ProductVersion)
	o.OS = strings.TrimSpace(o.OS)
	o.DisplayEnvironment = strings.TrimSpace(o.DisplayEnvironment)
	o.ObserverActor = strings.TrimSpace(o.ObserverActor)
	trimStrings(o.Preconditions)
	trimStrings(o.ExpectedBehavior)
	trimStrings(o.NegativeBehavior)
	for i := range o.Steps {
		o.Steps[i].Action = strings.TrimSpace(o.Steps[i].Action)
		o.Steps[i].Target = strings.TrimSpace(o.Steps[i].Target)
		o.Steps[i].ExpectedVisibleResult = strings.TrimSpace(o.Steps[i].ExpectedVisibleResult)
	}
	for i := range o.Evidence {
		o.Evidence[i].ArtifactID = strings.TrimSpace(o.Evidence[i].ArtifactID)
		o.Evidence[i].SHA256 = strings.ToLower(strings.TrimSpace(o.Evidence[i].SHA256))
		o.Evidence[i].MediaType = strings.ToLower(strings.TrimSpace(o.Evidence[i].MediaType))
		o.Evidence[i].Purpose = strings.TrimSpace(o.Evidence[i].Purpose)
	}
	for i := range spec.AcceptanceCriteria {
		c := &spec.AcceptanceCriteria[i]
		c.ID = strings.TrimSpace(c.ID)
		c.Description = strings.TrimSpace(c.Description)
		trimStrings(c.VerificationSteps)
		trimStrings(c.ExpectedBehavior)
		trimStrings(c.NegativeBehavior)
	}
	for i := range spec.IntegrationSeams {
		seam := &spec.IntegrationSeams[i]
		seam.ID = strings.TrimSpace(seam.ID)
		seam.EntryPoint = strings.TrimSpace(seam.EntryPoint)
		seam.VerificationLevel = strings.ToLower(strings.TrimSpace(seam.VerificationLevel))
		trimStrings(seam.AcceptanceCriteriaIDs)
		trimStrings(seam.VerificationSteps)
		for j := range seam.SourceEvidence {
			evidence := &seam.SourceEvidence[j]
			evidence.Path = strings.TrimSpace(evidence.Path)
			evidence.Symbol = strings.TrimSpace(evidence.Symbol)
			evidence.ExcerptSHA256 = strings.ToLower(strings.TrimSpace(evidence.ExcerptSHA256))
		}
		for j := range seam.MissingEdges {
			edge := &seam.MissingEdges[j]
			edge.Description = strings.TrimSpace(edge.Description)
			trimStrings(edge.RequiredFiles)
		}
	}
	if spec.ExecutionPlan != nil {
		p := spec.ExecutionPlan
		for i := range p.Subtasks {
			sub := &p.Subtasks[i]
			sub.Title = strings.TrimSpace(sub.Title)
			sub.Description = strings.TrimSpace(sub.Description)
			sub.AgentType = strings.TrimSpace(sub.AgentType)
			if sub.AgentType == "" {
				sub.AgentType = string(model.AgentCoder)
			}
			sub.Phase = strings.ToLower(strings.TrimSpace(sub.Phase))
			trimStrings(sub.Files)
			for j := range sub.ModuleBoundaries {
				boundary := &sub.ModuleBoundaries[j]
				boundary.Package = strings.TrimSpace(boundary.Package)
				boundary.Description = strings.TrimSpace(boundary.Description)
			}
			for j := range sub.InterfaceShapes {
				shape := &sub.InterfaceShapes[j]
				shape.Package = strings.TrimSpace(shape.Package)
				trimStrings(shape.Functions)
				trimStrings(shape.Types)
			}
			for j := range sub.InterfaceContracts {
				contract := &sub.InterfaceContracts[j]
				contract.Package = strings.TrimSpace(contract.Package)
				contract.Kind = strings.ToLower(strings.TrimSpace(contract.Kind))
				contract.State = strings.ToLower(strings.TrimSpace(contract.State))
				contract.OwnerFile = strings.TrimSpace(contract.OwnerFile)
				contract.Signature = strings.TrimSpace(contract.Signature)
				contract.Symbol = strings.TrimSpace(contract.Symbol)
				contract.ActionID = strings.TrimSpace(contract.ActionID)
				contract.CallbackSignature = strings.TrimSpace(contract.CallbackSignature)
				contract.Route = strings.TrimSpace(contract.Route)
				contract.TargetAction = strings.TrimSpace(contract.TargetAction)
				contract.Caller = strings.TrimSpace(contract.Caller)
				contract.Callee = strings.TrimSpace(contract.Callee)
			}
		}
		for i := range p.TDDExceptions {
			p.TDDExceptions[i].Reason = strings.TrimSpace(p.TDDExceptions[i].Reason)
		}
		for i := range p.Assumptions {
			p.Assumptions[i].Decision = strings.TrimSpace(p.Assumptions[i].Decision)
			p.Assumptions[i].WhyChosen = strings.TrimSpace(p.Assumptions[i].WhyChosen)
			trimStrings(p.Assumptions[i].Alternatives)
		}
	}
}

func trimStrings(values []string) {
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
}

func validateTaskSpec(spec orchdto.TaskSpecDTO, headerActor string) error {
	required := []struct{ name, value string }{
		{"title", spec.Title}, {"description", spec.Description}, {"actor", spec.Actor},
		{"idempotency_key", spec.IdempotencyKey},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if spec.Actor != headerActor {
		return errors.New("actor does not match X-Drem-Actor")
	}
	if spec.Observation == nil {
		return errors.New("observation is required")
	}
	o := spec.Observation
	observationRequired := []struct{ name, value string }{
		{"observation.session_id", o.SessionID}, {"observation.product", o.Product},
		{"observation.product_version", o.ProductVersion}, {"observation.os", o.OS},
		{"observation.display_environment", o.DisplayEnvironment}, {"observation.observer_actor", o.ObserverActor},
	}
	for _, field := range observationRequired {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if o.ObservedAt.IsZero() {
		return errors.New("observation.observed_at is required")
	}
	if len(o.Preconditions) == 0 || hasBlank(o.Preconditions) {
		return errors.New("observation.preconditions requires non-empty entries")
	}
	if len(o.Steps) == 0 {
		return errors.New("observation.steps requires at least one ordered step")
	}
	for i, step := range o.Steps {
		if step.Action == "" || step.ExpectedVisibleResult == "" {
			return fmt.Errorf("observation.steps[%d] requires action and expected_visible_result", i)
		}
	}
	if len(o.ExpectedBehavior) == 0 || hasBlank(o.ExpectedBehavior) {
		return errors.New("observation.expected_behavior requires non-empty entries")
	}
	if len(o.NegativeBehavior) == 0 || hasBlank(o.NegativeBehavior) {
		return errors.New("observation.negative_behavior requires non-empty entries")
	}
	if len(o.Evidence) == 0 {
		return errors.New("observation.evidence requires at least one content-addressed reference")
	}
	evidenceIDs := make(map[string]struct{}, len(o.Evidence))
	for i, evidence := range o.Evidence {
		if evidence.ArtifactID == "" || evidence.Purpose == "" {
			return fmt.Errorf("observation.evidence[%d] requires artifact_id and purpose", i)
		}
		if !stableReferenceID.MatchString(evidence.ArtifactID) {
			return fmt.Errorf("observation.evidence[%d].artifact_id must be a stable ID, not a path or URL", i)
		}
		if _, exists := evidenceIDs[evidence.ArtifactID]; exists {
			return fmt.Errorf("observation.evidence artifact_id %q is duplicated", evidence.ArtifactID)
		}
		evidenceIDs[evidence.ArtifactID] = struct{}{}
		decoded, err := hex.DecodeString(evidence.SHA256)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("observation.evidence[%d].sha256 must be a 64-character hex digest", i)
		}
		if !taskEvidenceMediaTypeAllowed(evidence.MediaType) {
			return fmt.Errorf("observation.evidence[%d].media_type must be image/*, video/*, text/*, or application/json", i)
		}
	}
	if len(spec.AcceptanceCriteria) == 0 {
		return errors.New("acceptance_criteria requires at least one criterion")
	}
	seen := make(map[string]struct{}, len(spec.AcceptanceCriteria))
	for i, criterion := range spec.AcceptanceCriteria {
		if criterion.ID == "" || criterion.Description == "" {
			return fmt.Errorf("acceptance_criteria[%d] requires id and description", i)
		}
		if _, exists := seen[criterion.ID]; exists {
			return fmt.Errorf("acceptance_criteria id %q is duplicated", criterion.ID)
		}
		if !stableReferenceID.MatchString(criterion.ID) {
			return fmt.Errorf("acceptance_criteria[%d].id must be a stable identifier", i)
		}
		seen[criterion.ID] = struct{}{}
		if len(criterion.VerificationSteps) == 0 || hasBlank(criterion.VerificationSteps) || len(criterion.ExpectedBehavior) == 0 || hasBlank(criterion.ExpectedBehavior) {
			return fmt.Errorf("acceptance_criteria[%d] requires non-empty verification_steps and expected_behavior", i)
		}
	}
	if len(spec.ProposedScope) == 0 || hasBlank(spec.ProposedScope) {
		return errors.New("proposed_scope requires non-empty entries")
	}
	if len(spec.Exclusions) == 0 || hasBlank(spec.Exclusions) {
		return errors.New("exclusions requires non-empty entries")
	}
	for _, optional := range []struct {
		name   string
		values []string
	}{
		{"dependencies", spec.Dependencies},
		{"uncertainty", spec.Uncertainty},
		{"open_questions", spec.OpenQuestions},
	} {
		if hasBlank(optional.values) {
			return fmt.Errorf("%s cannot contain blank entries", optional.name)
		}
	}
	if spec.ExecutionPlan != nil {
		if len(spec.OpenQuestions) > 0 {
			return errors.New("execution_plan requires open_questions to be resolved")
		}
		if err := validateExecutionPlan(*spec.ExecutionPlan, spec.ProposedScope); err != nil {
			return fmt.Errorf("execution_plan: %w", err)
		}
		if err := validateIntegrationSeams(spec); err != nil {
			return fmt.Errorf("integration_seams: %w", err)
		}
	}
	return nil
}

func taskEvidenceMediaTypeAllowed(mediaType string) bool {
	return strings.HasPrefix(mediaType, "image/") || strings.HasPrefix(mediaType, "video/") ||
		strings.HasPrefix(mediaType, "text/") || mediaType == "application/json"
}

func validateExecutionPlan(planDTO orchdto.TaskExecutionPlanDTO, proposedScope []string) error {
	if len(planDTO.Subtasks) == 0 || len(planDTO.Subtasks) > 8 {
		return errors.New("subtasks must contain between 1 and 8 entries")
	}
	scope := make(map[string]struct{}, len(proposedScope))
	for i, file := range proposedScope {
		if err := validatePlanPath(file); err != nil {
			return fmt.Errorf("proposed_scope[%d]: %w", i, err)
		}
		if _, exists := scope[file]; exists {
			return fmt.Errorf("proposed_scope path %q is duplicated", file)
		}
		scope[file] = struct{}{}
	}

	implCoverage := make(map[int]int)
	var implIndices []int
	integrationIndex := -1
	for i, sub := range planDTO.Subtasks {
		if sub.Title == "" || sub.Description == "" {
			return fmt.Errorf("subtasks[%d] requires title and description", i)
		}
		if sub.AgentType != string(model.AgentCoder) {
			return fmt.Errorf("subtasks[%d].agent_type must be %q", i, model.AgentCoder)
		}
		if sub.Phase != "test" && sub.Phase != "implementation" && sub.Phase != "integration" {
			return fmt.Errorf("subtasks[%d].phase must be test, implementation, or integration", i)
		}
		if len(sub.Files) == 0 || hasBlank(sub.Files) {
			return fmt.Errorf("subtasks[%d].files requires non-empty entries", i)
		}
		seenFiles := map[string]struct{}{}
		for j, file := range sub.Files {
			if err := validatePlanPath(file); err != nil {
				return fmt.Errorf("subtasks[%d].files[%d]: %w", i, j, err)
			}
			if _, ok := scope[file]; !ok {
				return fmt.Errorf("subtasks[%d] file %q is outside proposed_scope", i, file)
			}
			if _, exists := seenFiles[file]; exists {
				return fmt.Errorf("subtasks[%d] file %q is duplicated", i, file)
			}
			seenFiles[file] = struct{}{}
		}
		for _, dep := range sub.Dependencies {
			if dep < 0 || dep >= len(planDTO.Subtasks) || dep == i {
				return fmt.Errorf("subtasks[%d] has invalid dependency index %d", i, dep)
			}
			if sub.Phase == "test" && planDTO.Subtasks[dep].Phase == "implementation" {
				return fmt.Errorf("test subtask %d cannot depend on implementation subtask %d", i, dep)
			}
		}
		switch sub.Phase {
		case "test":
			if len(sub.TestsFor) != 1 {
				return fmt.Errorf("test subtask %d must reference exactly one implementation in tests_for", i)
			}
			impl := sub.TestsFor[0]
			if impl < 0 || impl >= len(planDTO.Subtasks) || planDTO.Subtasks[impl].Phase != "implementation" || impl <= i {
				return fmt.Errorf("test subtask %d has invalid tests_for index %d", i, impl)
			}
			if prior, exists := implCoverage[impl]; exists {
				return fmt.Errorf("test subtasks %d and %d both cover implementation subtask %d", prior, i, impl)
			}
			implCoverage[impl] = i
		case "implementation":
			implIndices = append(implIndices, i)
			if len(sub.TestsFor) > 0 {
				return fmt.Errorf("implementation subtask %d cannot set tests_for", i)
			}
			if err := validateImplementationDepth(i, sub, scope); err != nil {
				return err
			}
		case "integration":
			if integrationIndex != -1 {
				return errors.New("plan must contain exactly one integration subtask")
			}
			integrationIndex = i
			if len(sub.TestsFor) > 0 {
				return fmt.Errorf("integration subtask %d cannot set tests_for", i)
			}
		}
	}
	if hasPlanCycle(planDTO.Subtasks) {
		return errors.New("dependency cycle detected")
	}
	exceptions := map[int]struct{}{}
	for i, exception := range planDTO.TDDExceptions {
		if exception.SubtaskIndex < 0 || exception.SubtaskIndex >= len(planDTO.Subtasks) || planDTO.Subtasks[exception.SubtaskIndex].Phase != "implementation" {
			return fmt.Errorf("tdd_exceptions[%d] must reference an implementation subtask", i)
		}
		if exception.Reason == "" {
			return fmt.Errorf("tdd_exceptions[%d].reason is required", i)
		}
		exceptions[exception.SubtaskIndex] = struct{}{}
	}
	for _, impl := range implIndices {
		_, covered := implCoverage[impl]
		_, exempt := exceptions[impl]
		if covered == exempt {
			return fmt.Errorf("implementation subtask %d must have exactly one test or one TDD exception", impl)
		}
	}
	if integrationIndex != len(planDTO.Subtasks)-1 {
		return errors.New("the final subtask must be the single integration subtask")
	}
	deps := map[int]struct{}{}
	for _, dep := range planDTO.Subtasks[integrationIndex].Dependencies {
		deps[dep] = struct{}{}
	}
	for _, impl := range implIndices {
		if _, ok := deps[impl]; !ok {
			return fmt.Errorf("integration subtask must depend on implementation subtask %d", impl)
		}
	}
	for i, assumption := range planDTO.Assumptions {
		if assumption.Decision == "" || assumption.WhyChosen == "" || hasBlank(assumption.Alternatives) {
			return fmt.Errorf("assumptions[%d] requires decision, why_chosen, and non-blank alternatives", i)
		}
	}
	return nil
}

func validatePlanPath(file string) error {
	if file == "" || strings.Contains(file, "\\") || path.IsAbs(file) || path.Clean(file) != file || file == "." || strings.HasPrefix(file, "../") {
		return fmt.Errorf("%q must be a normalized repository-relative path", file)
	}
	return nil
}

func hasPlanCycle(subtasks []orchdto.TaskExecutionSubtaskDTO) bool {
	state := make([]uint8, len(subtasks))
	var visit func(int) bool
	visit = func(i int) bool {
		if state[i] == 1 {
			return true
		}
		if state[i] == 2 {
			return false
		}
		state[i] = 1
		for _, dep := range subtasks[i].Dependencies {
			if visit(dep) {
				return true
			}
		}
		state[i] = 2
		return false
	}
	for i := range subtasks {
		if visit(i) {
			return true
		}
	}
	return false
}

func executionPlanField(plan *orchdto.TaskExecutionPlanDTO) (model.JSONField, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("encode execution plan: %w", err)
	}
	var field model.JSONField
	if err := json.Unmarshal(raw, &field); err != nil {
		return nil, fmt.Errorf("store execution plan: %w", err)
	}
	return field, nil
}

func hasBlank(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func taskSpecHashes(spec orchdto.TaskSpecDTO) ([]byte, string, string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, "", "", err
	}
	request := sha256.Sum256(raw)
	fingerprintInput := struct {
		Title              string                               `json:"title"`
		Product            string                               `json:"product"`
		ProductVersion     string                               `json:"product_version"`
		Preconditions      []string                             `json:"preconditions"`
		Steps              []orchdto.ReferenceWorkflowStepDTO   `json:"steps"`
		ExpectedBehavior   []string                             `json:"expected_behavior"`
		NegativeBehavior   []string                             `json:"negative_behavior"`
		AcceptanceCriteria []orchdto.TaskAcceptanceCriterionDTO `json:"acceptance_criteria"`
		IntegrationSeams   []orchdto.TaskIntegrationSeamDTO     `json:"integration_seams"`
		ProposedScope      []string                             `json:"proposed_scope"`
		Exclusions         []string                             `json:"exclusions"`
	}{
		Title:              spec.Title,
		Product:            spec.Observation.Product,
		ProductVersion:     spec.Observation.ProductVersion,
		Preconditions:      spec.Observation.Preconditions,
		Steps:              spec.Observation.Steps,
		ExpectedBehavior:   spec.Observation.ExpectedBehavior,
		NegativeBehavior:   spec.Observation.NegativeBehavior,
		AcceptanceCriteria: spec.AcceptanceCriteria,
		IntegrationSeams:   spec.IntegrationSeams,
		ProposedScope:      spec.ProposedScope,
		Exclusions:         spec.Exclusions,
	}
	fingerprintJSON, err := json.Marshal(fingerprintInput)
	if err != nil {
		return nil, "", "", err
	}
	fingerprint := sha256.Sum256(fingerprintJSON)
	return raw, hex.EncodeToString(request[:]), hex.EncodeToString(fingerprint[:]), nil
}

func renderTaskSpecDescription(spec orchdto.TaskSpecDTO) string {
	var b strings.Builder
	b.WriteString(spec.Description)
	b.WriteString("\n\nReference observation: ")
	fmt.Fprintf(&b, "%s %s on %s (%s), session %s.\n", spec.Observation.Product, spec.Observation.ProductVersion, spec.Observation.OS, spec.Observation.DisplayEnvironment, spec.Observation.SessionID)
	writeTaskSpecList(&b, "Preconditions", spec.Observation.Preconditions)
	b.WriteString("Workflow:\n")
	for i, step := range spec.Observation.Steps {
		fmt.Fprintf(&b, "%d. %s", i+1, step.Action)
		if step.Target != "" {
			fmt.Fprintf(&b, " [%s]", step.Target)
		}
		fmt.Fprintf(&b, " -> %s\n", step.ExpectedVisibleResult)
	}
	writeTaskSpecList(&b, "Expected behavior", spec.Observation.ExpectedBehavior)
	writeTaskSpecList(&b, "Negative behavior", spec.Observation.NegativeBehavior)
	b.WriteString("Acceptance criteria:\n")
	for _, criterion := range spec.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s: %s\n", criterion.ID, criterion.Description)
		for _, step := range criterion.VerificationSteps {
			fmt.Fprintf(&b, "  verify: %s\n", step)
		}
		for _, expected := range criterion.ExpectedBehavior {
			fmt.Fprintf(&b, "  expect: %s\n", expected)
		}
		for _, negative := range criterion.NegativeBehavior {
			fmt.Fprintf(&b, "  must not: %s\n", negative)
		}
	}
	b.WriteString("Evidence references:\n")
	for _, evidence := range spec.Observation.Evidence {
		fmt.Fprintf(&b, "- %s sha256:%s (%s): %s\n", evidence.ArtifactID, evidence.SHA256, evidence.MediaType, evidence.Purpose)
	}
	if len(spec.IntegrationSeams) > 0 {
		b.WriteString("Source-backed production entrypoint seams:\n")
		for _, seam := range spec.IntegrationSeams {
			fmt.Fprintf(&b, "- %s [%s] entrypoint: %s; criteria: %s\n", seam.ID, seam.VerificationLevel, seam.EntryPoint, strings.Join(seam.AcceptanceCriteriaIDs, ", "))
			for _, evidence := range seam.SourceEvidence {
				fmt.Fprintf(&b, "  source: %s :: %s sha256:%s\n", evidence.Path, evidence.Symbol, evidence.ExcerptSHA256)
				fmt.Fprintf(&b, "  excerpt:\n%s\n", evidence.Excerpt)
			}
			for _, edge := range seam.MissingEdges {
				fmt.Fprintf(&b, "  missing edge: %s; required files: %s\n", edge.Description, strings.Join(edge.RequiredFiles, ", "))
			}
			for _, step := range seam.VerificationSteps {
				fmt.Fprintf(&b, "  runtime verify: %s\n", step)
			}
		}
	}
	b.WriteString("Proposed Canvas scope: ")
	b.WriteString(strings.Join(spec.ProposedScope, ", "))
	b.WriteString("\nExclusions: ")
	b.WriteString(strings.Join(spec.Exclusions, ", "))
	b.WriteByte('\n')
	writeTaskSpecList(&b, "Dependencies", spec.Dependencies)
	writeTaskSpecList(&b, "Uncertainty", spec.Uncertainty)
	writeTaskSpecList(&b, "Open questions", spec.OpenQuestions)
	return strings.TrimSpace(b.String())
}

func writeTaskSpecList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(label)
	b.WriteString(":\n")
	for _, value := range values {
		b.WriteString("- ")
		b.WriteString(value)
		b.WriteByte('\n')
	}
}
