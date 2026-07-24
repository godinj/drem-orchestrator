package projects

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileProjectDeploymentSpec_DerivesProjectLayoutAndTemplateDefaults(t *testing.T) {
	homeDir := filepath.Join(string(filepath.Separator), "home", "operator")
	spec, err := compileProjectDeploymentSpec(homeDir, "drem-orchestrator", TemplateData{
		Language:     LanguageGo,
		BareRepoPath: "/srv/git/drem-orchestrator.git",
	})
	require.NoError(t, err)

	projectDir := filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator")
	csuiteRoot := filepath.Join(projectDir, "csuite")
	require.Equal(t, homeDir, spec.HomeDir)
	require.Equal(t, "drem-orchestrator", spec.ProjectName)
	require.Equal(t, projectDir, spec.ProjectDir)
	require.Equal(t, filepath.Join(projectDir, "compose.yml"), spec.ComposePath)
	require.Equal(t, filepath.Join(projectDir, configFilename), spec.ConfigPath)
	require.Equal(t, filepath.Join(projectDir, "prompts"), spec.WorkerPromptRoot)
	require.Equal(t, filepath.Join(projectDir, "data"), spec.HostDataDir)
	require.Equal(t, filepath.Join(projectDir, "plan-packets"), spec.PlanPacketRoot)
	require.Equal(t,
		filepath.Join(csuiteRoot, "operator", "inbox", ".archive"),
		spec.CsuiteOperatorArchive)

	data := spec.TemplateData
	require.Equal(t, "drem-orchestrator", data.ProjectName)
	require.Equal(t, homeDir, data.HostHome)
	require.Equal(t, DefaultOrchImage, data.OrchImage)
	require.Equal(t, DefaultOrchHostPort, data.OrchHostPort)
	require.Equal(t, DefaultInferenceEndpoint, data.InferenceEndpoint)
	require.Equal(t, DefaultInferenceModel, data.InferenceModel)
	require.Equal(t, "object", data.InferenceToolArgumentsFormat)
	require.Equal(t, "auto_merge", data.IntegrationPolicy)
	require.Equal(t, "local_automated", data.VerificationPolicy)
	require.Equal(t, spec.ConfigPath, data.ConfigFilePath)
	require.Equal(t, filepath.Join(homeDir, ".claude", ".credentials.json"), data.WorkerCredsPath)
	require.Equal(t, filepath.Join(homeDir, ".codex", "auth.json"), data.WorkerCodexAuthPath)
	require.Equal(t, spec.WorkerPromptRoot, data.WorkerPromptRoot)
	require.Equal(t, csuiteRoot, data.CsuiteHomeRoot)
	require.Equal(t, spec.HostDataDir, data.HostDataDir)
	require.Equal(t, spec.PlanPacketRoot, data.PlanPacketRoot)
	require.Equal(t, filepath.Join(homeDir, ".drem", "csuite-watcher.token"), data.CsuiteWatcherTokenPath)
	require.Equal(t, "/etc/drem/host-exec.token", data.HostExecTokenPath)
	require.Equal(t, "go test ./...", data.TestCommand)
	require.Equal(t, "go vet ./...", data.CompileCommand)
}

func TestCompileProjectDeploymentSpec_DefaultsCppGateCommands(t *testing.T) {
	spec, err := compileProjectDeploymentSpec(t.TempDir(), "drem-canvas", TemplateData{
		Language:     LanguageCpp,
		BareRepoPath: "/srv/git/drem-canvas.git",
	})
	require.NoError(t, err)

	require.Equal(t,
		"cmake -S . -B build && cmake --build build && ctest --test-dir build --output-on-failure",
		spec.TemplateData.TestCommand)
	require.Equal(t,
		"cmake -S . -B build && cmake --build build",
		spec.TemplateData.CompileCommand)
}

func TestCompileProjectDeploymentSpec_IsolatesCsuiteRootPerProject(t *testing.T) {
	homeDir := filepath.Join(string(filepath.Separator), "home", "operator")
	first, err := compileProjectDeploymentSpec(homeDir, "drem-orchestrator", TemplateData{})
	require.NoError(t, err)
	second, err := compileProjectDeploymentSpec(homeDir, "drem-canvas", TemplateData{})
	require.NoError(t, err)

	require.Equal(t,
		filepath.Join(homeDir, ".drem", "projects", "drem-orchestrator", "csuite"),
		first.TemplateData.CsuiteHomeRoot)
	require.Equal(t,
		filepath.Join(homeDir, ".drem", "projects", "drem-canvas", "csuite"),
		second.TemplateData.CsuiteHomeRoot)
	require.NotEqual(t, first.TemplateData.CsuiteHomeRoot, second.TemplateData.CsuiteHomeRoot)
}

func TestCompileProjectDeploymentSpec_PreservesExplicitDeploymentOverrides(t *testing.T) {
	homeDir := t.TempDir()
	data := TemplateData{
		ProjectName:            "registry-name",
		Language:               LanguageGo,
		BareRepoPath:           "/srv/git/repo.git",
		DevMode:                true,
		OrchImage:              "custom-orch:latest",
		OrchHostPort:           19090,
		ConfigFilePath:         "/etc/drem/project.toml",
		HostHome:               "/mnt/operator",
		WorkerCredsPath:        "/secrets/claude.json",
		WorkerCodexAuthPath:    "/secrets/codex.json",
		WorkerPromptRoot:       "/var/drem/prompts",
		CsuiteHomeRoot:         "/var/drem/csuite",
		HostDataDir:            "/var/drem/data",
		PlanPacketRoot:         "/var/drem/plan-packets",
		CsuiteWatcherTokenPath: "/run/drem/watcher.token",
		HostExecTokenPath:      "/run/drem/host-exec.token",
		TestCommand:            "make test",
		CompileCommand:         "make build",
		InferenceEndpoint:      "http://host.docker.internal:18090/v1/chat/completions",
		InferenceModel:         "qwen3.6-27b-code",
	}

	spec, err := compileProjectDeploymentSpec(homeDir, "directory-name", data)
	require.NoError(t, err)

	projectDir := filepath.Join(homeDir, ".drem", "projects", "directory-name")
	require.Equal(t, projectDir, spec.ProjectDir)
	require.Equal(t, "/etc/drem/project.toml", spec.ConfigPath)
	require.Equal(t, "/var/drem/prompts", spec.WorkerPromptRoot)
	require.Equal(t, "/var/drem/data", spec.HostDataDir)
	require.Equal(t, "/var/drem/plan-packets", spec.PlanPacketRoot)
	require.Equal(t, filepath.Join("/var/drem/csuite", "operator", "inbox", ".archive"),
		spec.CsuiteOperatorArchive)

	compiled := spec.TemplateData
	require.Equal(t, data.ProjectName, compiled.ProjectName)
	require.Equal(t, data.OrchImage, compiled.OrchImage)
	require.Equal(t, data.OrchHostPort, compiled.OrchHostPort)
	require.Equal(t, data.ConfigFilePath, compiled.ConfigFilePath)
	require.Equal(t, data.HostHome, compiled.HostHome)
	require.Equal(t, data.WorkerCredsPath, compiled.WorkerCredsPath)
	require.Equal(t, data.WorkerCodexAuthPath, compiled.WorkerCodexAuthPath)
	require.Equal(t, data.WorkerPromptRoot, compiled.WorkerPromptRoot)
	require.Equal(t, data.CsuiteHomeRoot, compiled.CsuiteHomeRoot)
	require.Equal(t, data.HostDataDir, compiled.HostDataDir)
	require.Equal(t, data.PlanPacketRoot, compiled.PlanPacketRoot)
	require.Equal(t, data.CsuiteWatcherTokenPath, compiled.CsuiteWatcherTokenPath)
	require.Equal(t, data.HostExecTokenPath, compiled.HostExecTokenPath)
	require.Equal(t, data.TestCommand, compiled.TestCommand)
	require.Equal(t, data.CompileCommand, compiled.CompileCommand)
	require.Equal(t, data.InferenceEndpoint, compiled.InferenceEndpoint)
	require.Equal(t, data.InferenceModel, compiled.InferenceModel)
	require.Equal(t, "string", compiled.InferenceToolArgumentsFormat)
}
