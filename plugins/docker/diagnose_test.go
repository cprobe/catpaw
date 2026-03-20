package docker

import (
	"strings"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &DockerPlugin{}
	p.RegisterDiagnoseTools(registry)

	for _, name := range []string{"docker_ps", "docker_inspect"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeLocal {
			t.Fatalf("tool %q should be local scope", name)
		}
	}
}

func TestContainerNameHelper(t *testing.T) {
	c := containerListEntry{Names: []string{"/my-container"}}
	if containerName(c) != "my-container" {
		t.Fatal("should strip leading slash")
	}

	empty := containerListEntry{Id: "abc123def456"}
	name := containerName(empty)
	if name == "" {
		t.Fatal("should return something for empty names")
	}
}

func TestShortContainerIDHelper(t *testing.T) {
	full := "abc123def456789000"
	short := shortContainerID(full)
	if len(short) != 12 {
		t.Fatalf("expected 12 chars, got %d", len(short))
	}
	if !strings.HasPrefix(full, short) {
		t.Fatal("short ID should be prefix of full ID")
	}
}

func TestExecDockerInspectMissingName(t *testing.T) {
	_, err := execDockerInspect(nil, map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}
