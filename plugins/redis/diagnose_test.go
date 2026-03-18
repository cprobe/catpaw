package redis

import (
	"context"
	"strings"
	"testing"

	"github.com/cprobe/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)

	expectedTools := []string{
		"redis_info", "redis_slowlog", "redis_client_list", "redis_config_get",
		"redis_dbsize", "redis_latency", "redis_memory_doctor", "redis_memory_stats",
	}
	for _, name := range expectedTools {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeRemote {
			t.Fatalf("tool %q should be remote scope, got %v", name, tool.Scope)
		}
		if tool.RemoteExecute == nil {
			t.Fatalf("tool %q has nil RemoteExecute", name)
		}
	}

	if registry.ToolCount() != len(expectedTools) {
		t.Fatalf("expected %d tools, got %d", len(expectedTools), registry.ToolCount())
	}

	cats := registry.Categories()
	if len(cats) != 1 || cats[0] != "redis" {
		t.Fatalf("unexpected categories: %v", cats)
	}

	listing := registry.ListTools("redis")
	for _, name := range expectedTools {
		if !strings.Contains(listing, name) {
			t.Fatalf("ListTools output missing %q:\n%s", name, listing)
		}
	}
}

func TestFilterSensitiveConfig(t *testing.T) {
	input := "maxmemory\n104857600\nrequirepass\nmySecretPass\nsave\n900 1"
	filtered := filterSensitiveConfig(input)
	if strings.Contains(filtered, "mySecretPass") {
		t.Fatal("sensitive password should be redacted")
	}
	if !strings.Contains(filtered, "***REDACTED***") {
		t.Fatal("redaction marker missing")
	}
	if !strings.Contains(filtered, "104857600") {
		t.Fatal("non-sensitive value should be preserved")
	}
	if !strings.Contains(filtered, "900 1") {
		t.Fatal("non-sensitive save value should be preserved")
	}
}

func TestFilterSensitiveConfigMultipleKeys(t *testing.T) {
	input := "requirepass\nsecret1\nmasterauth\nsecret2\nmaxmemory\n1073741824"
	filtered := filterSensitiveConfig(input)
	if strings.Contains(filtered, "secret1") || strings.Contains(filtered, "secret2") {
		t.Fatal("all sensitive values should be redacted")
	}
	if strings.Count(filtered, "***REDACTED***") != 2 {
		t.Fatalf("expected 2 redactions, got %d", strings.Count(filtered, "***REDACTED***"))
	}
	if !strings.Contains(filtered, "1073741824") {
		t.Fatal("non-sensitive value should be preserved")
	}
}

func TestAccessorFactoryRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)

	// Factory should fail with wrong type
	_, err := registry.CreateAccessor(context.Background(), "redis", "not-an-instance")
	if err == nil {
		t.Fatal("expected error for wrong instanceRef type")
	}
	if !strings.Contains(err.Error(), "expected *Instance") {
		t.Fatalf("unexpected error: %v", err)
	}
}
