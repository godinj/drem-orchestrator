package personacontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const ComposeEnvVar = "DREM_PROJECT_COMPOSE"
const ComposeCommandEnvVar = "DREM_DOCKER_COMPOSE_CMD"

var (
	ErrNotConfigured = errors.New("persona container control is not configured")
	ErrUnknownTarget = errors.New("unknown persona control target")
	ErrUnknownAction = errors.New("unknown persona control action")
)

type Executor interface {
	Run(ctx context.Context, argv []string) error
}

type OutputExecutor interface {
	Executor
	RunOutput(ctx context.Context, argv []string) ([]byte, error)
}

type ExecExecutor struct{}

func (ExecExecutor) Run(ctx context.Context, argv []string) error {
	_, err := ExecExecutor{}.RunOutput(ctx, argv)
	return err
}

func (ExecExecutor) RunOutput(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s failed: %w: %s", argv[0], err, string(out))
	}
	return out, nil
}

type Controller struct {
	composePath    string
	composeCommand []string
	executor       Executor
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
	return NewWithCommand(os.Getenv(ComposeEnvVar), composeCommandFromEnv(), executor)
}

func New(composePath string, executor Executor) *Controller {
	return NewWithCommand(composePath, defaultComposeCommand(), executor)
}

func NewWithCommand(composePath string, composeCommand []string, executor Executor) *Controller {
	if executor == nil {
		executor = ExecExecutor{}
	}
	if len(composeCommand) == 0 {
		composeCommand = defaultComposeCommand()
	}
	return &Controller{composePath: composePath, composeCommand: append([]string(nil), composeCommand...), executor: executor}
}

func (c *Controller) ListContainers() ContainerList {
	items := make([]Container, 0, len(targetOrder))
	for _, target := range targetOrder {
		items = append(items, Container{Target: target, Service: targetServices[target][0], Status: "unknown"})
	}
	if c == nil || c.composePath == "" {
		return ContainerList{Available: false, Reason: "not configured", Items: items}
	}
	outExec, ok := c.executor.(OutputExecutor)
	if !ok {
		return ContainerList{Available: true, Compose: c.composePath, Items: items}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statuses, err := c.listServiceStatuses(ctx, outExec)
	if err != nil {
		return ContainerList{Available: true, Reason: err.Error(), Compose: c.composePath, Items: items}
	}
	for i := range items {
		if status := statuses[items[i].Service]; status != "" {
			items[i].Status = status
		}
	}
	return ContainerList{Available: true, Compose: c.composePath, Items: items}
}

func (c *Controller) listServiceStatuses(ctx context.Context, executor OutputExecutor) (map[string]string, error) {
	argv := append([]string(nil), c.composeCommand...)
	argv = append(argv, "-f", c.composePath, "ps", "-a", "--format", "json")
	for _, target := range targetOrder {
		argv = append(argv, targetServices[target]...)
	}
	out, err := executor.RunOutput(ctx, argv)
	if err != nil {
		return nil, err
	}

	statuses := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var row struct {
			Service string `json:"Service"`
			State   string `json:"State"`
			Status  string `json:"Status"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, err
		}
		if row.State != "" {
			statuses[row.Service] = row.State
		} else if row.Status != "" {
			statuses[row.Service] = row.Status
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (c *Controller) Control(ctx context.Context, target, action string) (ControlResult, error) {
	argv, services, err := BuildArgvWithCommand(c.composeCommand, c.composePath, target, action)
	if err != nil {
		return ControlResult{}, err
	}
	if err := c.executor.Run(ctx, argv); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Status: "ok", Target: target, Action: action, Services: services}, nil
}

func BuildArgv(composePath, target, action string) ([]string, []string, error) {
	return BuildArgvWithCommand(defaultComposeCommand(), composePath, target, action)
}

func BuildArgvWithCommand(composeCommand []string, composePath, target, action string) ([]string, []string, error) {
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
	if len(composeCommand) == 0 {
		composeCommand = defaultComposeCommand()
	}

	argv := append([]string(nil), composeCommand...)
	argv = append(argv, "-f", composePath)
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

func defaultComposeCommand() []string {
	return []string{"docker", "compose"}
}

func composeCommandFromEnv() []string {
	cmd := strings.Fields(os.Getenv(ComposeCommandEnvVar))
	if len(cmd) == 0 {
		return defaultComposeCommand()
	}
	return cmd
}

var targetOrder = []string{"mike", "alex", "seth", "kyle"}

var targetServices = map[string][]string{
	"mike": {"csuite-mike"},
	"alex": {"csuite-alex"},
	"seth": {"csuite-seth"},
	"kyle": {"csuite-kyle"},
	"all":  {"csuite-mike", "csuite-alex", "csuite-seth", "csuite-kyle"},
}
