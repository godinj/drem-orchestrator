package projects

import (
	"fmt"
	"os"
	"path/filepath"
)

// projectDeploymentSpec is the package-local boundary between project
// registration inputs and the derived host layout consumed by renderers and
// provisioning code.
type projectDeploymentSpec struct {
	HomeDir               string
	ProjectName           string
	ProjectDir            string
	ComposePath           string
	ConfigPath            string
	WorkerPromptRoot      string
	HostDataDir           string
	PlanPacketRoot        string
	CsuiteOperatorArchive string
	TemplateData          TemplateData
}

func compileProjectDeploymentSpec(homeDir, projectName string, data TemplateData) (projectDeploymentSpec, error) {
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return projectDeploymentSpec{}, fmt.Errorf("resolve home dir: %w", err)
		}
		homeDir = h
	}
	if data.ProjectName == "" {
		data.ProjectName = projectName
	}

	projectDir := filepath.Join(homeDir, ".drem", "projects", projectName)
	configPath := data.ConfigFilePath
	if configPath == "" {
		configPath = filepath.Join(projectDir, configFilename)
		data.ConfigFilePath = configPath
	}

	data = compileTemplateDefaults(data, homeDir, projectName)
	spec := projectDeploymentSpec{
		HomeDir:          homeDir,
		ProjectName:      projectName,
		ProjectDir:       projectDir,
		ComposePath:      filepath.Join(projectDir, "compose.yml"),
		ConfigPath:       configPath,
		WorkerPromptRoot: data.WorkerPromptRoot,
		HostDataDir:      data.HostDataDir,
		PlanPacketRoot:   data.PlanPacketRoot,
		TemplateData:     data,
	}
	if data.CsuiteHomeRoot != "" {
		spec.CsuiteOperatorArchive = filepath.Join(data.CsuiteHomeRoot, "operator", "inbox", ".archive")
	}
	return spec, nil
}

func compileTemplateDefaults(data TemplateData, fallbackHome, fallbackProject string) TemplateData {
	if data.IntegrationPolicy == "" {
		data.IntegrationPolicy = "auto_merge"
	}
	if data.VerificationPolicy == "" {
		data.VerificationPolicy = "local_automated"
	}
	if data.PlanReviewPolicy == "" {
		data.PlanReviewPolicy = "manual"
	}
	if data.TestReviewPolicy == "" {
		data.TestReviewPolicy = "manual"
	}
	if data.InferenceEndpoint == "" {
		data.InferenceEndpoint = DefaultInferenceEndpoint
	}
	if data.OrchImage == "" {
		if data.DevMode {
			data.OrchImage = DefaultOrchDevImage
		} else {
			data.OrchImage = DefaultOrchImage
		}
	}
	if data.OrchHostPort == 0 {
		data.OrchHostPort = DefaultOrchHostPort
	}
	if data.HostHome == "" {
		if fallbackHome != "" {
			data.HostHome = fallbackHome
		} else if h, err := os.UserHomeDir(); err == nil {
			data.HostHome = h
		}
	}
	projectName := data.ProjectName
	if projectName == "" {
		projectName = fallbackProject
	}
	if data.WorkerCredsPath == "" && data.HostHome != "" {
		data.WorkerCredsPath = filepath.Join(data.HostHome, ".claude", ".credentials.json")
	}
	if data.WorkerCodexAuthPath == "" && data.HostHome != "" {
		data.WorkerCodexAuthPath = filepath.Join(data.HostHome, ".codex", "auth.json")
	}
	if data.WarmAgentTokenPath == "" && data.HostHome != "" {
		data.WarmAgentTokenPath = filepath.Join(data.HostHome, ".drem", warmAgentTokenFilename)
	}
	if data.WorkerPromptRoot == "" && data.HostHome != "" && projectName != "" {
		data.WorkerPromptRoot = filepath.Join(data.HostHome, ".drem", "projects", projectName, "prompts")
	}
	if data.CsuiteHomeRoot == "" && data.HostHome != "" && projectName != "" {
		data.CsuiteHomeRoot = filepath.Join(data.HostHome, ".drem", "projects", projectName, "csuite")
	}
	if data.HostDataDir == "" && data.HostHome != "" && projectName != "" {
		data.HostDataDir = filepath.Join(data.HostHome, ".drem", "projects", projectName, "data")
	}
	if data.PlanPacketRoot == "" && data.HostHome != "" && projectName != "" {
		data.PlanPacketRoot = filepath.Join(data.HostHome, ".drem", "projects", projectName, "plan-packets")
	}
	if data.CsuiteWatcherTokenPath == "" && data.HostHome != "" {
		data.CsuiteWatcherTokenPath = filepath.Join(data.HostHome, ".drem", "csuite-watcher.token")
	}
	if data.HostExecTokenPath == "" {
		data.HostExecTokenPath = "/etc/drem/host-exec.token"
	}
	if data.TestCommand == "" {
		data.TestCommand = defaultTestCommand(data.Language)
	}
	if data.CompileCommand == "" {
		data.CompileCommand = defaultCompileCommand(data.Language)
	}
	return data
}

// DefaultInferenceEndpoint is the in-stack GQ endpoint used by normal Linux
// deployments. External-inference deployments set an explicit project value.
const DefaultInferenceEndpoint = "http://gq:8090/v1/chat/completions"

func defaultTestCommand(language string) string {
	switch language {
	case LanguageGo:
		return "go test ./..."
	case LanguageCpp:
		return "cmake -S . -B build && cmake --build build && ctest --test-dir build --output-on-failure"
	default:
		return ""
	}
}

func defaultCompileCommand(language string) string {
	switch language {
	case LanguageGo:
		return "go vet ./..."
	case LanguageCpp:
		return "cmake -S . -B build && cmake --build build"
	default:
		return ""
	}
}
