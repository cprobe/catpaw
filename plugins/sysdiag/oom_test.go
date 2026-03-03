package sysdiag

import (
	"strings"
	"testing"
)

func TestParseOOMEvents(t *testing.T) {
	// Simulated dmesg output with OOM kill events (kernel 5.x+ style)
	raw := `2026-03-02T10:00:01+0800 node1 kernel: [12345.678] java invoked oom-killer: gfp_mask=0xcc0, order=0
2026-03-02T10:00:01+0800 node1 kernel: [12345.679] oom_score_adj=0
2026-03-02T10:00:01+0800 node1 kernel: [12345.680] Out of memory: Killed process 4567 (java), UID 1000, total-vm:8388608kB, anon-rss:4194304kB, file-rss:1024kB
2026-03-02T10:15:00+0800 node1 kernel: [13000.100] python3 invoked oom-killer: gfp_mask=0xcc0, order=0
2026-03-02T10:15:00+0800 node1 kernel: [13000.101] oom_score_adj=500
2026-03-02T10:15:00+0800 node1 kernel: [13000.102] Out of memory: Killed process 7890 (python3), UID 0, total-vm:2097152kB, anon-rss:1048576kB, file-rss:512kB`

	events := parseOOMEvents(raw)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	ev0 := events[0]
	if ev0.pid != 4567 {
		t.Errorf("event[0].pid = %d, want 4567", ev0.pid)
	}
	if ev0.killed != "java" {
		t.Errorf("event[0].killed = %q, want 'java'", ev0.killed)
	}
	if ev0.uid != 1000 {
		t.Errorf("event[0].uid = %d, want 1000", ev0.uid)
	}
	// RSS = anon-rss + file-rss = 4194304 + 1024 = 4195328 kB; pages = 4195328/4 = 1048832
	if ev0.rssPages != 1048832 {
		t.Errorf("event[0].rssPages = %d, want 1048832", ev0.rssPages)
	}
	if ev0.score != 0 {
		t.Errorf("event[0].score = %d, want 0", ev0.score)
	}

	ev1 := events[1]
	if ev1.pid != 7890 {
		t.Errorf("event[1].pid = %d, want 7890", ev1.pid)
	}
	if ev1.score != 500 {
		t.Errorf("event[1].score = %d, want 500", ev1.score)
	}
}

func TestParseOOMEventsKernel3Style(t *testing.T) {
	// Kernel 3.x style: no UID field
	raw := `[12345.678] Killed process 1234 (mysqld) total-vm:4096000kB, anon-rss:2048000kB, file-rss:100kB`

	events := parseOOMEvents(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].killed != "mysqld" {
		t.Errorf("killed = %q, want 'mysqld'", events[0].killed)
	}
	if events[0].uid != 0 {
		t.Errorf("uid = %d, want 0 (not present in old format)", events[0].uid)
	}
	// RSS = anon + file = 2048000 + 100
	if events[0].rssPages != 512025 {
		t.Errorf("rssPages = %d, want 512025", events[0].rssPages)
	}
}

func TestParseOOMEventsKernel6Style(t *testing.T) {
	// Kernel 6.x: shmem-rss, UID: (colon), UID after shmem-rss
	raw := `[12345.680] Out of memory: Killed process 9999 (postgres) total-vm:2484556kB, anon-rss:143224kB, file-rss:0kB, shmem-rss:452kB, UID:1011 pgtables:588kB oom_score_adj:900`

	events := parseOOMEvents(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].pid != 9999 {
		t.Errorf("pid = %d, want 9999", events[0].pid)
	}
	if events[0].killed != "postgres" {
		t.Errorf("killed = %q, want 'postgres'", events[0].killed)
	}
	if events[0].uid != 1011 {
		t.Errorf("uid = %d, want 1011 (UID: format)", events[0].uid)
	}
	// RSS = anon + file + shmem = 143224 + 0 + 452 = 143676 kB
	if events[0].rssPages != 35919 {
		t.Errorf("rssPages = %d, want 35919", events[0].rssPages)
	}
}

func TestParseOOMEventsEmpty(t *testing.T) {
	events := parseOOMEvents("some random kernel log\nno oom here\n")
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestFormatOOMEvents(t *testing.T) {
	events := []oomEvent{
		{timestamp: "2026-03-02T10:00", pid: 1234, killed: "java", rssPages: 1000, score: 0},
	}
	out := formatOOMEvents(events, 1, "24h")
	if !strings.Contains(out, "java") {
		t.Fatal("expected 'java' in output")
	}
	if !strings.Contains(out, "1234") {
		t.Fatal("expected PID in output")
	}
	if !strings.Contains(out, "4000") {
		t.Fatal("expected RSS in KB (1000 pages * 4) in output")
	}
}

func TestTruncName(t *testing.T) {
	if truncName("short", 20) != "short" {
		t.Fatal("should not truncate short name")
	}
	long := "very-long-process-name-here"
	result := truncName(long, 15)
	if len(result) > 15 {
		t.Fatalf("truncated result too long: %d", len(result))
	}
	if !strings.HasSuffix(result, "~") {
		t.Fatal("truncated result should end with ~")
	}
}
