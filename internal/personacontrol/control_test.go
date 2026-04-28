package personacontrol

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBuildArgvAllowlistedActions(t *testing.T) {
	compose := "/tmp/project-compose.yml"
	tests := []struct {
		name   string
		target string
		action string
		want   []string
	}{
		{
			name:   "stop",
			target: "seth",
			action: "stop",
			want:   []string{"docker", "compose", "-f", compose, "stop", "csuite-seth"},
		},
		{
			name:   "start",
			target: "alex",
			action: "start",
			want:   []string{"docker", "compose", "-f", compose, "up", "-d", "--no-deps", "csuite-alex"},
		},
		{
			name:   "recreate",
			target: "kyle",
			action: "recreate",
			want:   []string{"docker", "compose", "-f", compose, "up", "-d", "--no-deps", "--force-recreate", "csuite-kyle"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := BuildArgv(compose, tt.target, tt.action)
			if err != nil {
				t.Fatalf("BuildArgv: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("argv = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildArgvRejectsUnknownTargetAndAction(t *testing.T) {
	if _, _, err := BuildArgv("compose.yml", "ross", "stop"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("unknown target err = %v, want %v", err, ErrUnknownTarget)
	}
	if _, _, err := BuildArgv("compose.yml", "mike", "rebuild"); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("unknown action err = %v, want %v", err, ErrUnknownAction)
	}
}

func TestBuildArgvAllExpandsOnlyPersonaServices(t *testing.T) {
	got, services, err := BuildArgv("compose.yml", "all", "start")
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	wantServices := []string{"csuite-mike", "csuite-alex", "csuite-seth", "csuite-kyle"}
	if !reflect.DeepEqual(services, wantServices) {
		t.Fatalf("services = %#v, want %#v", services, wantServices)
	}
	for _, forbidden := range []string{"sglang", "gq", "registry"} {
		for _, arg := range got {
			if arg == forbidden {
				t.Fatalf("argv contains non-persona service %q: %#v", forbidden, got)
			}
		}
	}
}

func TestBuildArgvRequiresComposePath(t *testing.T) {
	if _, _, err := BuildArgv("", "mike", "stop"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing compose err = %v, want %v", err, ErrNotConfigured)
	}
}

func TestControllerUsesExecutor(t *testing.T) {
	exec := &recordingExecutor{}
	controller := New("compose.yml", exec)

	result, err := controller.Control(context.Background(), "mike", "stop")
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if result.Status != "ok" || result.Services[0] != "csuite-mike" {
		t.Fatalf("unexpected result: %+v", result)
	}
	want := []string{"docker", "compose", "-f", "compose.yml", "stop", "csuite-mike"}
	if !reflect.DeepEqual(exec.argv, want) {
		t.Fatalf("executor argv = %#v, want %#v", exec.argv, want)
	}
}

type recordingExecutor struct {
	argv []string
}

func (e *recordingExecutor) Run(_ context.Context, argv []string) error {
	e.argv = append([]string(nil), argv...)
	return nil
}
