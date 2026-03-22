package redis_sentinel

import (
	"context"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestRegisterDiagnoseTools(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	p := &RedisSentinelPlugin{}
	p.RegisterDiagnoseTools(registry)

	expectedTools := []string{
		"sentinel_overview",
		"sentinel_master_health",
		"sentinel_replicas",
		"sentinel_sentinels",
		"sentinel_info",
	}
	for _, name := range expectedTools {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Scope != diagnose.ToolScopeRemote {
			t.Fatalf("tool %q should be remote scope", name)
		}
	}
	if registry.ToolCount() != len(expectedTools) {
		t.Fatalf("expected %d tools, got %d", len(expectedTools), registry.ToolCount())
	}
	if got := registry.RunPreCollector(context.Background(), "redis_sentinel", nil); got != "" {
		t.Fatalf("expected empty precollector result with nil accessor, got %q", got)
	}
	hints := registry.GetDiagnoseHints("redis_sentinel")
	if !strings.Contains(hints, "sentinel_master_health") {
		t.Fatalf("unexpected hints: %s", hints)
	}
}

func TestSentinelOverviewTool(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{
			"mymaster": {
				Name:              "mymaster",
				IP:                "10.0.0.20",
				Port:              "6379",
				Flags:             "master",
				Quorum:            "2",
				NumSlaves:         2,
				NumOtherSentinels: 2,
			},
		},
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"sentinel.local:26379"},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisSentinelPlugin{}
	p.RegisterDiagnoseTools(registry)
	tool, ok := registry.Get("sentinel_overview")
	if !ok {
		t.Fatal("sentinel_overview not registered")
	}
	accessor, err := registry.CreateAccessor(context.Background(), "redis_sentinel", ins, "sentinel.local:26379")
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	session.SetInstanceRef(ins)
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[ROLE]") || !strings.Contains(out, "mymaster") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestSentinelMasterHealthTool(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{
			"mymaster": {
				Name:              "mymaster",
				IP:                "10.0.0.20",
				Port:              "6379",
				Flags:             "master,s_down",
				Status:            "s_down",
				Quorum:            "2",
				NumSlaves:         1,
				NumOtherSentinels: 2,
				Replicas: []fakeSentinelNode{
					{Name: "replica-1", IP: "10.0.0.21", Port: "6379", Flags: "slave"},
				},
				Sentinels: []fakeSentinelNode{
					{Name: "sentinel-2", IP: "10.0.0.11", Port: "26379", Flags: "sentinel"},
				},
			},
		},
		ckquorum: map[string]string{
			"mymaster": "NOQUORUM 1 usable Sentinels",
		},
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"sentinel.local:26379"},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisSentinelPlugin{}
	p.RegisterDiagnoseTools(registry)
	tool, ok := registry.Get("sentinel_master_health")
	if !ok {
		t.Fatal("sentinel_master_health not registered")
	}
	accessor, err := registry.CreateAccessor(context.Background(), "redis_sentinel", ins, "sentinel.local:26379")
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	session.SetInstanceRef(ins)
	defer session.Close()

	out, err := tool.RemoteExecute(context.Background(), session, map[string]string{"master": "mymaster"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[MASTER HEALTH]") || !strings.Contains(out, "replicas: 1") || !strings.Contains(out, "NOQUORUM") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
