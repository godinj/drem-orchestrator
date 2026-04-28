package personacontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

const ComposeEnvVar = "DREM_PROJECT_COMPOSE"

var (
	ErrNotConfigured = errors.New("persona container control is not configured")
	ErrUnknownTarget = errors.New("unknown persona control target")
	ErrUnknownAction = errors.New("unknown persona control action")
)

type Executor interface {
	Run(ctx context.Context, argv []string) error
}

type ExecExecutor struct{}

func (ExecExecutor) Run(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", argv[0], err, string(out))
	}
	return nil
}

type Controller struct {
	composePath string
	executor    Executor
}

type Container struct {
	Target  string `json:"target"`
	Service string `json:"service"`
	Status  string `json:"status"`
}

type ContainerList struct {
	Available bool        `json:"available"`
	Reason    string      `json:"reason,omitempty"`
	Compose   string      `json:"compose,omitempty"`
	Items     []Container `json:"items"`
}

type ControlResult struct {
	Status   string   `json:"status"`
	Target   string   `json:"target"`
	Action   string   `json:"action"`
	Services []string `json:"services"`
}

func NewFromEnv(executor Executor) *Controller {
	return New(os.Getenv(ComposeEnvVar), executor)
}

func New(composePath string, executor Executor) *Controller {
	if executor == nil {
		executor = ExecExecutor{}
	}
	return &Controller{composePath: composePath, executor: executor}
}

func (c *Controller) ListContainers() ContainerList {
	items := make([]Container, 0, len(targetOrder))
	for _, target := range targetOrder {
		items = append(items, Container{Target: target, Service: targetServices[target][0], Status: "unknown"})
	}
	if c == nil || c.composePath == "" {
		return ContainerList{Available: false, Reason: "not configured", Items: items}
	}
	return ContainerList{Available: true, Compose: c.composePath, Items: items}
}

func (c *Controller) Control(ctx context.Context, target, action string) (ControlResult, error) {
	argv, services, err := BuildArgv(c.composePath, target, action)
	if err != nil {
		return ControlResult{}, err
	}
	if err := c.executor.Run(ctx, argv); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Status: "ok", Target: target, Action: action, Services: services}, nil
}

func BuildArgv(composePath, target, action string) ([]string, []string, error) {
	services, ok := targetServices[target]
	if !ok {
		return nil, nil, ErrUnknownTarget
	}
	if action != "stop" && action != "start" && action != "recreate" {
		return nil, nil, ErrUnknownAction
	}
	if composePath == "" {
		return nil, nil, ErrNotConfigured
	}

	argv := []string{"docker", "compose", "-f", composePath}
	switch action {
	case "stop":
		argv = append(argv, "stop")
	case "start":
		argv = append(argv, "up", "-d", "--no-deps")
	case "recreate":
		argv = append(argv, "up", "-d", "--no-deps", "--force-recreate")
	}
	argv = append(argv, services...)
	return argv, append([]string(nil), services...), nil
}

var targetOrder = []string{"mike", "alex", "seth", "kyle"}

var targetServices = map[string][]string{
	"mike": {"csuite-mike"},
	"alex": {"csuite-alex"},
	"seth": {"csuite-seth"},
	"kyle": {"csuite-kyle"},
	"all":  {"csuite-mike", "csuite-alex", "csuite-seth", "csuite-kyle"},
}
