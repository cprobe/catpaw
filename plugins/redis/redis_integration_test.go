//go:build integration

package redis

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func integrationRedisTarget() string {
	if v := strings.TrimSpace(os.Getenv("REDIS_CLUSTER_TARGET")); v != "" {
		return v
	}
	return "127.0.0.1:7000"
}

func integrationRedisPassword() string {
	if v := os.Getenv("REDIS_CLUSTER_PASSWORD"); v != "" {
		return v
	}
	return "catpaw-test"
}

func TestRedisClusterIntegrationGather(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		Targets:  []string{integrationRedisTarget()},
		Password: integrationRedisPassword(),
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	byCheck := collectByCheck(events)
	for _, check := range []string{"redis::connectivity", "redis::cluster_state", "redis::cluster_topology"} {
		event, ok := byCheck[check]
		if !ok {
			t.Fatalf("missing event for %s", check)
		}
		if event.EventStatus != types.EventStatusOk {
			t.Fatalf("%s: expected Ok, got %s (%s)", check, event.EventStatus, event.Description)
		}
	}
}

func TestRedisClusterIntegrationClusterInfoTool(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		Targets:  []string{integrationRedisTarget()},
		Password: integrationRedisPassword(),
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

	accessor, err := registry.CreateAccessor(context.Background(), "redis", ins, integrationRedisTarget())
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cluster_state: ok") {
		t.Fatalf("expected cluster_state in output, got:\n%s", out)
	}
	if !strings.Contains(out, "[CLUSTER NODES]") {
		t.Fatalf("expected cluster nodes output, got:\n%s", out)
	}
}

func TestRedisClusterIntegrationBigkeysTool(t *testing.T) {
	initTestConfig(t)

	if os.Getenv("REDIS_CLUSTER_BIGKEYS_READY") != "1" {
		t.Skip("set REDIS_CLUSTER_BIGKEYS_READY=1 after seeding cluster keys to run bigkeys integration test")
	}

	ins := &Instance{
		Targets:  []string{integrationRedisTarget()},
		Password: integrationRedisPassword(),
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

	accessor, err := registry.CreateAccessor(context.Background(), "redis", ins, integrationRedisTarget())
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, map[string]string{
		"sample_keys": "200",
		"topn":        "10",
		"match":       "catpaw:*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[TOP") {
		t.Fatalf("expected bigkeys output, got:\n%s", out)
	}
}
