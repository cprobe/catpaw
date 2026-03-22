package redis

import (
	"context"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)

	expectedTools := []string{
		"redis_info", "redis_cluster_info", "redis_slowlog", "redis_client_list", "redis_config_get",
		"redis_bigkeys_scan", "redis_query_peer",
		"redis_latency", "redis_memory_analysis",
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

	removedTools := []string{"redis_dbsize", "redis_memory_doctor", "redis_memory_stats"}
	for _, name := range removedTools {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("tool %q should have been removed/merged but is still registered", name)
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

func TestPreCollectorRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)

	result := registry.RunPreCollector(context.Background(), "redis", nil)
	if result != "" {
		t.Fatal("PreCollector with nil accessor should return empty string")
	}

	result = registry.RunPreCollector(context.Background(), "unknown", nil)
	if result != "" {
		t.Fatal("PreCollector for unregistered plugin should return empty string")
	}
}

func TestDiagnoseHintsRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)

	hints := registry.GetDiagnoseHints("redis")
	if hints == "" {
		t.Fatal("DiagnoseHints for redis should not be empty")
	}
	if !strings.Contains(hints, "内存告警") {
		t.Fatal("DiagnoseHints should contain memory alert route")
	}
	if !strings.Contains(hints, "Cluster 告警") {
		t.Fatal("DiagnoseHints should contain cluster alert route")
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
	_, err := registry.CreateAccessor(context.Background(), "redis", "not-an-instance", "127.0.0.1:6379")
	if err == nil {
		t.Fatal("expected error for wrong instanceRef type")
	}
	if !strings.Contains(err.Error(), "expected *Instance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisClusterInfoToolStandalone(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		redisMode: redisModeStandalone,
		role:      "master",
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)
	tool, ok := registry.Get("redis_cluster_info")
	if !ok {
		t.Fatal("redis_cluster_info not registered")
	}
	accessor, err := registry.CreateAccessor(context.Background(), "redis", ins, "redis.local:6379")
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not cluster mode") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRedisBigkeysScanTool(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role: "master",
		keys: map[string]fakeRedisKey{
			"cart:1":    {Type: "hash", Size: 4096},
			"cart:2":    {Type: "hash", Size: 8192},
			"session:1": {Type: "string", Size: 128},
			"user:100":  {Type: "string", Size: 1024},
		},
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisPlugin{}
	p.RegisterDiagnoseTools(registry)
	tool, ok := registry.Get("redis_bigkeys_scan")
	if !ok {
		t.Fatal("redis_bigkeys_scan not registered")
	}
	accessor, err := registry.CreateAccessor(context.Background(), "redis", ins, "redis.local:6379")
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, map[string]string{
		"sample_keys": "10",
		"topn":        "2",
		"match":       "cart:*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sampled_keys: 2") {
		t.Fatalf("expected sampled key count, got:\n%s", out)
	}
	if !strings.Contains(out, "cart:2") {
		t.Fatalf("expected largest cart key in output, got:\n%s", out)
	}
	if strings.Contains(out, "session:1") {
		t.Fatalf("unexpected non-matching key in output:\n%s", out)
	}
}
