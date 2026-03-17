//go:build integration

package redis_sentinel

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func integrationSentinelTarget() string {
	if v := strings.TrimSpace(os.Getenv("REDIS_SENTINEL_TARGET")); v != "" {
		return v
	}
	return "127.0.0.1:26379"
}

func integrationSentinelPassword() string {
	return strings.TrimSpace(os.Getenv("REDIS_SENTINEL_PASSWORD"))
}

func integrationSentinelMaster() string {
	if v := strings.TrimSpace(os.Getenv("REDIS_SENTINEL_MASTER")); v != "" {
		return v
	}
	return "mymaster"
}

func waitForSentinelReady(t *testing.T, target, password, master string) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		acc, err := NewSentinelAccessor(SentinelAccessorConfig{
			Target:      target,
			Password:    password,
			Timeout:     3 * time.Second,
			ReadTimeout: 2 * time.Second,
		})
		if err == nil {
			role, roleErr := acc.Role()
			masters, mastersErr := acc.SentinelMasters()
			ckquorum, quorumErr := acc.SentinelCKQuorum(master)
			_ = acc.Close()

			if roleErr == nil && mastersErr == nil && quorumErr == nil && role == "sentinel" && len(masters) > 0 && strings.Contains(strings.ToLower(ckquorum), "usable sentinels") {
				return
			}
			if quorumErr != nil {
				lastErr = quorumErr
			} else if mastersErr != nil {
				lastErr = mastersErr
			} else {
				lastErr = roleErr
			}
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("sentinel test environment not ready before deadline: %v", lastErr)
}

func TestRedisSentinelIntegrationGather(t *testing.T) {
	initSentinelTestConfig(t)

	target := integrationSentinelTarget()
	password := integrationSentinelPassword()
	master := integrationSentinelMaster()
	waitForSentinelReady(t, target, password, master)

	ins := &Instance{
		Targets:  []string{target},
		Password: password,
		Masters:  []MasterRef{{Name: master}},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events, got %d", len(events))
	}

	assertCheckOk := func(check string) {
		ev := findEvent(events, check)
		if ev == nil {
			t.Fatalf("missing event %s", check)
		}
		if ev.EventStatus != types.EventStatusOk {
			t.Fatalf("%s: expected Ok, got %s (%s)", check, ev.EventStatus, ev.Description)
		}
	}
	assertMasterCheckOk := func(check string) {
		ev := findMasterEvent(events, check, master)
		if ev == nil {
			t.Fatalf("missing event %s for master %s", check, master)
		}
		if ev.EventStatus != types.EventStatusOk {
			t.Fatalf("%s: expected Ok, got %s (%s)", check, ev.EventStatus, ev.Description)
		}
	}

	assertCheckOk("redis_sentinel::connectivity")
	assertCheckOk("redis_sentinel::role")
	assertCheckOk("redis_sentinel::masters_overview")
	assertMasterCheckOk("redis_sentinel::ckquorum")
	assertMasterCheckOk("redis_sentinel::master_addr_resolution")
}

func TestRedisSentinelIntegrationDiagnoseTools(t *testing.T) {
	initSentinelTestConfig(t)

	target := integrationSentinelTarget()
	password := integrationSentinelPassword()
	master := integrationSentinelMaster()
	waitForSentinelReady(t, target, password, master)

	ins := &Instance{
		Targets:  []string{target},
		Password: password,
		Masters:  []MasterRef{{Name: master}},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisSentinelPlugin{}
	p.RegisterDiagnoseTools(registry)

	accessor, err := registry.CreateAccessor(context.Background(), "redis_sentinel", ins, target)
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	session.SetInstanceRef(ins)
	defer session.Close()

	overview, ok := registry.Get("sentinel_overview")
	if !ok {
		t.Fatal("sentinel_overview not registered")
	}
	out, err := overview.RemoteExecute(context.Background(), session, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[ROLE]") || !strings.Contains(out, master) {
		t.Fatalf("unexpected overview output:\n%s", out)
	}

	health, ok := registry.Get("sentinel_master_health")
	if !ok {
		t.Fatal("sentinel_master_health not registered")
	}
	out, err = health.RemoteExecute(context.Background(), session, map[string]string{"master": master})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[MASTER HEALTH]") || !strings.Contains(out, "[CKQUORUM]") {
		t.Fatalf("unexpected master health output:\n%s", out)
	}
}

func TestRedisSentinelIntegrationSnapshot(t *testing.T) {
	initSentinelTestConfig(t)

	target := integrationSentinelTarget()
	password := integrationSentinelPassword()
	master := integrationSentinelMaster()
	if os.Getenv("REDIS_SENTINEL_SKIP_READY") != "1" {
		waitForSentinelReady(t, target, password, master)
	}

	ins := &Instance{
		Targets:  []string{target},
		Password: password,
		Masters:  []MasterRef{{Name: master}},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	sort.Slice(events, func(i, j int) bool {
		ci := events[i].Labels["check"]
		cj := events[j].Labels["check"]
		if ci == cj {
			return events[i].Labels["master_name"] < events[j].Labels["master_name"]
		}
		return ci < cj
	})

	t.Log("[gather events]")
	for _, ev := range events {
		t.Logf("check=%s master=%s status=%s desc=%s attrs=%v",
			ev.Labels["check"], ev.Labels["master_name"], ev.EventStatus, ev.Description, ev.Attrs)
	}

	registry := diagnose.NewToolRegistry()
	p := &RedisSentinelPlugin{}
	p.RegisterDiagnoseTools(registry)

	accessor, err := registry.CreateAccessor(context.Background(), "redis_sentinel", ins, target)
	if err != nil {
		t.Fatal(err)
	}
	session := &diagnose.DiagnoseSession{Accessor: accessor}
	session.SetInstanceRef(ins)
	defer session.Close()

	overview, _ := registry.Get("sentinel_overview")
	out, err := overview.RemoteExecute(context.Background(), session, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("[sentinel_overview]")
	t.Log(out)

	health, _ := registry.Get("sentinel_master_health")
	out, err = health.RemoteExecute(context.Background(), session, map[string]string{"master": master})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("[sentinel_master_health]")
	t.Log(out)
}
