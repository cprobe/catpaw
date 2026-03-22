package redis_sentinel

import (
	"bufio"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	clogger "github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
	"go.uber.org/zap"
)

func initSentinelTestConfig(t *testing.T) {
	t.Helper()
	if config.Config == nil {
		tmpDir := t.TempDir()
		config.Config = &config.ConfigType{
			ConfigDir: tmpDir,
			StateDir:  tmpDir,
		}
	}
	if clogger.Logger == nil {
		l, _ := zap.NewDevelopment()
		clogger.Logger = l.Sugar()
	}
}

type fakeSentinelServer struct {
	mu  sync.RWMutex
	cfg fakeSentinelConfig
}

type fakeSentinelConfig struct {
	username        string
	password        string
	role            string
	info            string
	pingDelay       time.Duration
	masters         map[string]fakeSentinelMaster
	ckquorum        map[string]string
	replicasErrors  map[string]string
	sentinelsErrors map[string]string
}

type fakeSentinelMaster struct {
	Name              string
	IP                string
	Port              string
	Flags             string
	Status            string
	Quorum            string
	NumSlaves         int
	NumOtherSentinels int
	Replicas          []fakeSentinelNode
	Sentinels         []fakeSentinelNode
}

type fakeSentinelNode struct {
	Name  string
	IP    string
	Port  string
	Flags string
}

func startFakeSentinelServer(t *testing.T, cfg fakeSentinelConfig) *fakeSentinelServer {
	t.Helper()
	return &fakeSentinelServer{cfg: cfg}
}

func (s *fakeSentinelServer) Close() {}

func (s *fakeSentinelServer) Dial(network, address string) (net.Conn, error) {
	client, server := net.Pipe()
	go s.handleConn(server)
	return client, nil
}

func (s *fakeSentinelServer) handleConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	authed := cfg.password == ""

	for {
		args, err := readRESPArray(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			return
		}
		cmd := strings.ToUpper(args[0])
		if !authed && cmd != "AUTH" {
			writeRESPError(conn, "NOAUTH Authentication required.")
			continue
		}
		switch cmd {
		case "AUTH":
			if cfg.username != "" {
				if len(args) != 3 || args[1] != cfg.username || args[2] != cfg.password {
					writeRESPError(conn, "WRONGPASS invalid username-password pair")
					continue
				}
			} else {
				if len(args) != 2 || args[1] != cfg.password {
					writeRESPError(conn, "WRONGPASS invalid password")
					continue
				}
			}
			authed = true
			writeRESPSimpleString(conn, "OK")
		case "PING":
			if cfg.pingDelay > 0 {
				time.Sleep(cfg.pingDelay)
			}
			writeRESPSimpleString(conn, "PONG")
		case "ROLE":
			role := cfg.role
			if role == "" {
				role = "sentinel"
			}
			writeRESPValue(conn, []any{role})
		case "INFO":
			info := cfg.info
			if info == "" {
				info = "# Sentinel\r\nsentinel_tilt:0\r\n"
			}
			writeRESPBulkString(conn, info)
		case "SENTINEL":
			if len(args) < 2 {
				writeRESPError(conn, "ERR wrong number of arguments for 'sentinel' command")
				continue
			}
			sub := strings.ToUpper(args[1])
			switch sub {
			case "MASTERS":
				writeRESPValue(conn, buildMastersReply(cfg.masters))
			case "MASTER":
				if len(args) < 3 {
					writeRESPError(conn, "ERR missing master name")
					continue
				}
				master, ok := cfg.masters[args[2]]
				if !ok {
					writeRESPError(conn, "ERR No such master with that name")
					continue
				}
				writeRESPValue(conn, buildMasterReply(master))
			case "REPLICAS":
				if len(args) < 3 {
					writeRESPError(conn, "ERR missing master name")
					continue
				}
				if msg, ok := cfg.replicasErrors[args[2]]; ok && msg != "" {
					writeRESPError(conn, msg)
					continue
				}
				master, ok := cfg.masters[args[2]]
				if !ok {
					writeRESPError(conn, "ERR No such master with that name")
					continue
				}
				writeRESPValue(conn, buildNodesReply(master.Replicas))
			case "SENTINELS":
				if len(args) < 3 {
					writeRESPError(conn, "ERR missing master name")
					continue
				}
				if msg, ok := cfg.sentinelsErrors[args[2]]; ok && msg != "" {
					writeRESPError(conn, msg)
					continue
				}
				master, ok := cfg.masters[args[2]]
				if !ok {
					writeRESPError(conn, "ERR No such master with that name")
					continue
				}
				writeRESPValue(conn, buildNodesReply(master.Sentinels))
			case "CKQUORUM":
				if len(args) < 3 {
					writeRESPError(conn, "ERR missing master name")
					continue
				}
				if msg, ok := cfg.ckquorum[args[2]]; ok && msg != "" {
					writeRESPError(conn, msg)
					continue
				}
				writeRESPSimpleString(conn, "OK 3 usable Sentinels. Quorum and failover authorization can be reached")
			case "GET-MASTER-ADDR-BY-NAME":
				if len(args) < 3 {
					writeRESPError(conn, "ERR missing master name")
					continue
				}
				master, ok := cfg.masters[args[2]]
				if !ok || master.IP == "" || master.Port == "" {
					writeRESPNil(conn)
					continue
				}
				writeRESPValue(conn, []any{master.IP, master.Port})
			default:
				writeRESPError(conn, "ERR unknown subcommand for SENTINEL")
			}
		default:
			writeRESPError(conn, "ERR unsupported command")
		}
	}
}

func buildMastersReply(masters map[string]fakeSentinelMaster) []any {
	names := make([]string, 0, len(masters))
	for name := range masters {
		names = append(names, name)
	}
	sort.Strings(names)
	reply := make([]any, 0, len(names))
	for _, name := range names {
		reply = append(reply, buildMasterReply(masters[name]))
	}
	return reply
}

func buildMasterReply(master fakeSentinelMaster) []any {
	return []any{
		"name", master.Name,
		"ip", master.IP,
		"port", master.Port,
		"flags", master.Flags,
		"status", master.Status,
		"quorum", fmt.Sprintf("%s", fallback(master.Quorum, "2")),
		"num-slaves", fmt.Sprintf("%d", master.NumSlaves),
		"num-other-sentinels", fmt.Sprintf("%d", master.NumOtherSentinels),
	}
}

func buildNodesReply(nodes []fakeSentinelNode) []any {
	reply := make([]any, 0, len(nodes))
	for _, node := range nodes {
		reply = append(reply, []any{
			"name", node.Name,
			"ip", node.IP,
			"port", node.Port,
			"flags", node.Flags,
		})
	}
	return reply
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '*' {
		return nil, fmt.Errorf("expected array, got %q", prefix)
	}
	countLine, err := readRESPLine(reader)
	if err != nil {
		return nil, err
	}
	count := 0
	fmt.Sscanf(countLine, "%d", &count)
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if prefix, err = reader.ReadByte(); err != nil {
			return nil, err
		}
		if prefix != '$' {
			return nil, fmt.Errorf("expected bulk string, got %q", prefix)
		}
		sizeLine, err := readRESPLine(reader)
		if err != nil {
			return nil, err
		}
		size := 0
		fmt.Sscanf(sizeLine, "%d", &size)
		buf := make([]byte, size+2)
		if _, err := reader.Read(buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func writeRESPSimpleString(conn net.Conn, s string) {
	_, _ = conn.Write([]byte("+" + s + "\r\n"))
}

func writeRESPBulkString(conn net.Conn, s string) {
	_, _ = conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(s), s)))
}

func writeRESPError(conn net.Conn, s string) {
	_, _ = conn.Write([]byte("-" + s + "\r\n"))
}

func writeRESPNil(conn net.Conn) {
	_, _ = conn.Write([]byte("*-1\r\n"))
}

func writeRESPValue(conn net.Conn, v any) {
	switch x := v.(type) {
	case string:
		writeRESPBulkString(conn, x)
	case []any:
		_, _ = conn.Write([]byte(fmt.Sprintf("*%d\r\n", len(x))))
		for _, elem := range x {
			writeRESPValue(conn, elem)
		}
	default:
		writeRESPBulkString(conn, fmt.Sprintf("%v", x))
	}
}

func drainEvents(q *safe.Queue[*types.Event]) []*types.Event {
	return q.PopBackAll()
}

func findEvent(events []*types.Event, check string) *types.Event {
	for _, event := range events {
		if event.Labels["check"] == check {
			return event
		}
	}
	return nil
}

func findMasterEvent(events []*types.Event, check, master string) *types.Event {
	for _, event := range events {
		if event.Labels["check"] == check && event.Labels["master_name"] == master {
			return event
		}
	}
	return nil
}

func TestInitNormalizesTargets(t *testing.T) {
	initSentinelTestConfig(t)

	ins := &Instance{
		Targets: []string{"sentinel.local"},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}
	if got := ins.Targets[0]; got != "sentinel.local:26379" {
		t.Fatalf("expected normalized target, got %q", got)
	}
}

func TestGatherHealthy(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{
			"mymaster": {
				Name:              "mymaster",
				IP:                "10.0.0.20",
				Port:              "6379",
				Flags:             "master",
				Status:            "ok",
				Quorum:            "2",
				NumSlaves:         2,
				NumOtherSentinels: 2,
				Replicas: []fakeSentinelNode{
					{Name: "replica-1", IP: "10.0.0.21", Port: "6379", Flags: "slave"},
				},
				Sentinels: []fakeSentinelNode{
					{Name: "sentinel-2", IP: "10.0.0.11", Port: "26379", Flags: "sentinel"},
					{Name: "sentinel-3", IP: "10.0.0.12", Port: "26379", Flags: "sentinel"},
				},
			},
		},
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"sentinel.local:26379"},
		Masters:  []MasterRef{{Name: "mymaster"}},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := drainEvents(q)

	if ev := findEvent(events, "redis_sentinel::connectivity"); ev == nil || ev.EventStatus != types.EventStatusOk {
		t.Fatalf("expected connectivity ok event, got %+v", ev)
	}
	if ev := findEvent(events, "redis_sentinel::role"); ev == nil || ev.EventStatus != types.EventStatusOk {
		t.Fatalf("expected role ok event, got %+v", ev)
	}
	if ev := findMasterEvent(events, "redis_sentinel::ckquorum", "mymaster"); ev == nil || ev.EventStatus != types.EventStatusOk {
		t.Fatalf("expected ckquorum ok event, got %+v", ev)
	}
	if ev := findMasterEvent(events, "redis_sentinel::master_addr_resolution", "mymaster"); ev == nil || ev.EventStatus != types.EventStatusOk {
		t.Fatalf("expected addr resolution ok event, got %+v", ev)
	}
}

func TestGatherMissingConfiguredMaster(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{},
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"sentinel.local:26379"},
		Masters:  []MasterRef{{Name: "mymaster"}},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := drainEvents(q)

	ev := findMasterEvent(events, "redis_sentinel::ckquorum", "mymaster")
	if ev == nil {
		t.Fatal("expected missing-master ckquorum event")
	}
	if ev.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical, got %+v", ev)
	}
	if !strings.Contains(ev.Description, "not present in SENTINEL MASTERS") {
		t.Fatalf("unexpected description: %s", ev.Description)
	}
}

func TestGatherWithoutConfiguredMastersSkipsPerMasterChecks(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{
			"mymaster": {
				Name:              "mymaster",
				IP:                "10.0.0.20",
				Port:              "6379",
				Flags:             "master",
				Status:            "ok",
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

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := drainEvents(q)

	if ev := findEvent(events, "redis_sentinel::masters_overview"); ev == nil {
		t.Fatal("expected masters_overview event")
	} else if !strings.Contains(ev.Description, "per-master checks are skipped") {
		t.Fatalf("unexpected description: %s", ev.Description)
	}

	for _, check := range []string{
		"redis_sentinel::ckquorum",
		"redis_sentinel::master_sdown",
		"redis_sentinel::master_odown",
		"redis_sentinel::master_addr_resolution",
	} {
		if ev := findMasterEvent(events, check, "mymaster"); ev != nil {
			t.Fatalf("expected no per-master event for %s, got %+v", check, ev)
		}
	}
}

func TestGatherTopologyQueryFailureIsCritical(t *testing.T) {
	initSentinelTestConfig(t)

	srv := startFakeSentinelServer(t, fakeSentinelConfig{
		masters: map[string]fakeSentinelMaster{
			"mymaster": {
				Name:              "mymaster",
				IP:                "10.0.0.20",
				Port:              "6379",
				Flags:             "master",
				Status:            "ok",
				Quorum:            "2",
				NumSlaves:         2,
				NumOtherSentinels: 2,
			},
		},
		replicasErrors: map[string]string{
			"mymaster": "ERR replicas query failed",
		},
		sentinelsErrors: map[string]string{
			"mymaster": "ERR sentinels query failed",
		},
	})
	defer srv.Close()

	enabled := true
	ins := &Instance{
		Targets:  []string{"sentinel.local:26379"},
		Masters:  []MasterRef{{Name: "mymaster"}},
		dialFunc: srv.Dial,
		KnownReplicas: ThresholdCheck{
			Enabled: &enabled,
			WarnLt:  2,
		},
		KnownSentinels: ThresholdCheck{
			Enabled: &enabled,
			WarnLt:  2,
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := drainEvents(q)

	if ev := findMasterEvent(events, "redis_sentinel::known_replicas", "mymaster"); ev == nil {
		t.Fatal("expected known_replicas event")
	} else if ev.EventStatus != types.EventStatusCritical || !strings.Contains(ev.Description, "failed to query known replicas") {
		t.Fatalf("unexpected known_replicas event: %+v", ev)
	}

	if ev := findMasterEvent(events, "redis_sentinel::known_sentinels", "mymaster"); ev == nil {
		t.Fatal("expected known_sentinels event")
	} else if ev.EventStatus != types.EventStatusCritical || !strings.Contains(ev.Description, "failed to query known sentinels") {
		t.Fatalf("unexpected known_sentinels event: %+v", ev)
	}
}
