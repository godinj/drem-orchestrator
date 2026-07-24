// Package projects manages the host-wide project registry located at
// ~/.drem/projects.toml. The registry lists every project that Drem knows
// about alongside its bare repository path, language, orchestrator API URL,
// and any per-agent container image overrides.
//
// The registry is host-side state only. It is not replicated into any
// project's SQLite database and no running orchestrator reads it — only
// Kyle (the reporting agent) and the `drem project` CLI consult it. See
// docs/prd-containerization.md user stories 2, 30, 31, and 50 for the full
// motivation.
package projects

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/godinj/drem-orchestrator/internal/model"
)

// Supported project languages. Image selection is driven by this field, so
// the allowed set is intentionally narrow (see PRD user story 33).
const (
	LanguageGo  = "go"
	LanguageCpp = "cpp"
)

// Project is a single entry in the host-wide registry.
type Project struct {
	// Name is the project's unique identifier. It is used as the registry
	// key and as the directory name under ~/.drem/projects/<name>/.
	Name string `toml:"name"`
	// BareRepoPath is the absolute path to the project's bare Git
	// repository. Must exist on disk and must be a bare repo.
	BareRepoPath string `toml:"bare_repo_path"`
	// Language selects the per-language worker image. Currently "go" or
	// "cpp".
	Language string `toml:"language"`
	// OrchURL is the HTTP endpoint that Kyle (and the TUI, eventually)
	// uses to read live orchestrator state for this project.
	OrchURL string `toml:"orch_url"`
	// InferenceEndpoint is the OpenAI-compatible chat-completions endpoint
	// injected into direct workers. Empty retains the in-stack GQ default.
	InferenceEndpoint string `toml:"inference_endpoint,omitempty"`
	// InferenceModel is the served model ID requested from InferenceEndpoint.
	// Empty retains the backward-compatible Gemma default.
	InferenceModel string `toml:"inference_model,omitempty"`
	// IntegrationPolicy controls whether a verified delivery is merged
	// automatically or waits for explicit integration authorization.
	IntegrationPolicy string `toml:"integration_policy,omitempty"`
	// VerificationPolicy controls whether delivery verification is performed
	// locally by the orchestrator or acknowledged by an external host verifier.
	VerificationPolicy string `toml:"verification_policy,omitempty"`
	// Review policies choose whether plan/test gates remain manual or allow a
	// fail-closed SGLang reviewer to approve structurally safe results.
	PlanReviewPolicy string `toml:"plan_review_policy,omitempty"`
	TestReviewPolicy string `toml:"test_review_policy,omitempty"`
	// Gate commands are project-specific executable contracts. Canvas uses its
	// repository wrapper rather than the generic CMake fallback.
	TestCommand    string `toml:"test_command,omitempty"`
	CompileCommand string `toml:"compile_command,omitempty"`
	// ContainerImageOverrides maps agent type ("coder", "merger",
	// "csuite-mike", ...) to a specific image tag. Agent types without an
	// override use the language-derived default.
	ContainerImageOverrides map[string]string `toml:"container_image_overrides,omitempty"`
	// OrchHostPort is the host-side loopback port bound to the
	// orchestrator container's :8080. Allocated at registration time via
	// Registry.AllocateOrchHostPort so that concurrent projects do not
	// collide on 127.0.0.1:8080.
	OrchHostPort int `toml:"orch_host_port,omitempty"`
	// SharedToken authenticates agentmon POST /internal/logs requests
	// for this project. Generated once per registration via
	// projects.NewSharedToken. Only orch and agentmon ever learn it.
	SharedToken string `toml:"shared_token,omitempty"`
	// DevMode records that the project was registered with --dev so
	// regeneration keeps the dev orchestrator image and /src bind-mount.
	DevMode bool `toml:"dev_mode,omitempty"`
}

// Registry is the in-memory view of ~/.drem/projects.toml.
type Registry struct {
	// Path is the file this registry was loaded from (and will be saved
	// back to).
	Path string `toml:"-"`
	// Projects is the ordered list of registered projects.
	Projects []Project `toml:"projects"`
}

// DefaultPath returns the canonical registry path, $HOME/.drem/projects.toml.
// It falls back to "./.drem/projects.toml" only when $HOME is unset, which
// should never happen in practice but keeps the function total.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".drem", "projects.toml")
	}
	return filepath.Join(home, ".drem", "projects.toml")
}

// Load reads the registry at path. A missing file is not an error: Load
// returns an empty registry in that case so that first-time callers can
// call Add and Save without a preliminary write.
func Load(path string) (*Registry, error) {
	r := &Registry{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("read registry %q: %w", path, err)
	}
	if len(data) == 0 {
		return r, nil
	}
	if err := toml.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("parse registry %q: %w", path, err)
	}
	r.Path = path
	return r, nil
}

// Save writes the registry to its Path atomically (tempfile + rename).
// The enclosing directory is created with mode 0o755 if it does not
// already exist.
func (r *Registry) Save() error {
	if r.Path == "" {
		return errors.New("registry path is empty")
	}
	dir := filepath.Dir(r.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create registry dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".projects.toml.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if the rename never happens.
	defer func() { _ = os.Remove(tmpName) }()

	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(r); err != nil {
		tmp.Close()
		return fmt.Errorf("encode registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// os.CreateTemp defaults to 0600; widen to 0644 so the file is readable
	// by non-root users inside Kyle's container when bind-mounted.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, r.Path); err != nil {
		return fmt.Errorf("rename temp to %q: %w", r.Path, err)
	}
	return nil
}

// Add validates p and appends it. Returns an error if a project with the
// same name already exists, if a required field is missing, or if the
// bare repo path is not a bare Git repository.
func (r *Registry) Add(p Project) error {
	if err := validateProject(p); err != nil {
		return err
	}
	if _, ok := r.Get(p.Name); ok {
		return fmt.Errorf("project %q already exists", p.Name)
	}
	r.Projects = append(r.Projects, p)
	return nil
}

// AllocateOrchHostPort returns the next free loopback port for a newly
// registering project, starting at DefaultOrchHostPort (8080) and
// incrementing past any ports already taken. It does not mutate the
// registry — callers must copy the returned value onto Project.OrchHostPort
// before Add/Save.
func (r *Registry) AllocateOrchHostPort() int {
	used := make(map[int]bool, len(r.Projects))
	for _, p := range r.Projects {
		if p.OrchHostPort > 0 {
			used[p.OrchHostPort] = true
		} else if port := OrchHostPortFromURL(p.OrchURL); port > 0 {
			// Older registry entries predate OrchHostPort. Their URL is still
			// authoritative enough to keep a fresh registration from colliding.
			used[port] = true
		}
	}
	for port := DefaultOrchHostPort; ; port++ {
		if !used[port] {
			return port
		}
	}
}

// OrchHostPortFromURL returns the explicit TCP port in an orchestrator URL,
// or zero when the URL is malformed or omits a port.
func OrchHostPortFromURL(raw string) int {
	u, err := url.Parse(raw)
	if err != nil || u.Port() == "" {
		return 0
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

// Remove deletes the project with the given name. Returns an error if no
// such project exists.
func (r *Registry) Remove(name string) error {
	for i, p := range r.Projects {
		if p.Name == name {
			r.Projects = append(r.Projects[:i], r.Projects[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("project %q not found", name)
}

// Get returns a pointer to the project with the given name and true, or
// nil and false if no such project exists. The pointer aliases the
// underlying slice entry; callers must not retain it across mutations of
// the registry.
func (r *Registry) Get(name string) (*Project, bool) {
	for i := range r.Projects {
		if r.Projects[i].Name == name {
			return &r.Projects[i], true
		}
	}
	return nil, false
}

// validateProject checks a Project for required fields, a supported
// language, and a bare-repo-shaped BareRepoPath.
func validateProject(p Project) error {
	if p.Name == "" {
		return errors.New("project name is required")
	}
	if p.BareRepoPath == "" {
		return errors.New("bare_repo_path is required")
	}
	if p.Language == "" {
		return errors.New("language is required")
	}
	if p.OrchURL == "" {
		return errors.New("orch_url is required")
	}
	if p.IntegrationPolicy != "" {
		if _, err := model.ParseIntegrationPolicy(p.IntegrationPolicy); err != nil {
			return err
		}
	}
	if p.VerificationPolicy != "" {
		if _, err := model.ParseVerificationPolicy(p.VerificationPolicy); err != nil {
			return err
		}
	}
	for label, raw := range map[string]string{"plan_review_policy": p.PlanReviewPolicy, "test_review_policy": p.TestReviewPolicy} {
		if raw != "" {
			if _, err := model.ParseReviewGatePolicy(raw); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
	}
	if !validLanguage(p.Language) {
		return fmt.Errorf("unsupported language %q (want %q or %q)",
			p.Language, LanguageGo, LanguageCpp)
	}
	if err := checkBareRepo(p.BareRepoPath); err != nil {
		return err
	}
	if p.InferenceEndpoint != "" {
		parsed, err := url.ParseRequestURI(p.InferenceEndpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("inference_endpoint must be an absolute http(s) URL")
		}
	}
	if p.InferenceModel != "" && strings.ContainsAny(p.InferenceModel, "\"\r\n") {
		return fmt.Errorf("inference_model must not contain quotes or newlines")
	}
	return nil
}

// validLanguage reports whether l is one of the supported languages.
func validLanguage(l string) bool {
	return l == LanguageGo || l == LanguageCpp
}

// checkBareRepo verifies that path points at a bare git repository by
// looking for the HEAD file and the objects/ directory. This matches the
// minimum layout that git rev-parse --is-bare-repository recognises
// without shelling out.
func checkBareRepo(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("bare_repo_path %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bare_repo_path %q is not a directory", path)
	}
	headPath := filepath.Join(path, "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		return fmt.Errorf("bare_repo_path %q has no HEAD (not a bare repo)", path)
	}
	objectsPath := filepath.Join(path, "objects")
	objInfo, err := os.Stat(objectsPath)
	if err != nil || !objInfo.IsDir() {
		return fmt.Errorf("bare_repo_path %q has no objects/ directory (not a bare repo)", path)
	}
	return nil
}
