package redis

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	clogger "github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	tlscfg "github.com/cprobe/catpaw/pkg/tls"
	"github.com/cprobe/catpaw/types"
	"go.uber.org/zap"
)

func initTestConfig(t *testing.T) {
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

type fakeRedisServer struct {
	mu  sync.RWMutex
	cfg fakeRedisConfig
}

type fakeRedisConfig struct {
	username         string
	password         string
	role             string
	masterLinkStatus string
	masterHost       string
	masterPort       string
	connectedSlaves  int
	connectedClients int
	blockedClients   int
	rejectedConn     int
	evictedKeys      uint64
	expiredKeys      uint64
	opsPerSecond     int
	usedMemory       int64
	maxMemory        int64
	loading          int
	rdbLastBgsave    string
	rdbInProgress    int
	aofEnabled       int
	aofLastWrite     string
	aofRewrite       int
	pingDelay        time.Duration
}

func startFakeRedisServer(t *testing.T, cfg fakeRedisConfig) *fakeRedisServer {
	t.Helper()
	return &fakeRedisServer{cfg: cfg}
}

func (s *fakeRedisServer) Close() {
}

func (s *fakeRedisServer) Dial(network, address string) (net.Conn, error) {
	client, server := net.Pipe()
	go s.handleConn(server)
	return client, nil
}

func (s *fakeRedisServer) SetConfig(cfg fakeRedisConfig) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *fakeRedisServer) handleConn(conn net.Conn) {
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
		case "SELECT":
			writeRESPSimpleString(conn, "OK")
		case "PING":
			if cfg.pingDelay > 0 {
				time.Sleep(cfg.pingDelay)
			}
			writeRESPSimpleString(conn, "PONG")
		case "INFO":
			section := ""
			if len(args) > 1 {
				section = strings.ToLower(args[1])
			}
			switch section {
			case "replication":
				writeRESPBulkString(conn, fmt.Sprintf("# Replication\r\nrole:%s\r\nmaster_link_status:%s\r\nmaster_host:%s\r\nmaster_port:%s\r\nconnected_slaves:%d\r\n",
					cfg.role, cfg.masterLinkStatus, cfg.masterHost, cfg.masterPort, cfg.connectedSlaves))
			case "clients":
				writeRESPBulkString(conn, fmt.Sprintf("# Clients\r\nconnected_clients:%d\r\nblocked_clients:%d\r\n",
					cfg.connectedClients, cfg.blockedClients))
			case "memory":
				writeRESPBulkString(conn, fmt.Sprintf("# Memory\r\nused_memory:%d\r\nmaxmemory:%d\r\n",
					cfg.usedMemory, cfg.maxMemory))
			case "stats":
				writeRESPBulkString(conn, fmt.Sprintf("# Stats\r\nrejected_connections:%d\r\nevicted_keys:%d\r\nexpired_keys:%d\r\ninstantaneous_ops_per_sec:%d\r\n",
					cfg.rejectedConn, cfg.evictedKeys, cfg.expiredKeys, cfg.opsPerSecond))
			case "persistence":
				writeRESPBulkString(conn, fmt.Sprintf("# Persistence\r\nloading:%d\r\nrdb_last_bgsave_status:%s\r\nrdb_bgsave_in_progress:%d\r\naof_enabled:%d\r\naof_last_write_status:%s\r\naof_rewrite_in_progress:%d\r\n",
					cfg.loading, cfg.rdbLastBgsave, cfg.rdbInProgress, cfg.aofEnabled, cfg.aofLastWrite, cfg.aofRewrite))
			default:
				writeRESPBulkString(conn, "")
			}
		default:
			writeRESPError(conn, "ERR unknown command")
		}
	}
}

func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %q", line)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}

	ret := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimSuffix(strings.TrimSuffix(hdr, "\n"), "\r")
		if !strings.HasPrefix(hdr, "$") {
			return nil, fmt.Errorf("expected bulk string, got %q", hdr)
		}
		size, err := strconv.Atoi(strings.TrimPrefix(hdr, "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		ret = append(ret, string(buf[:size]))
	}

	return ret, nil
}

func writeRESPSimpleString(conn net.Conn, s string) {
	_, _ = conn.Write([]byte("+" + s + "\r\n"))
}

func writeRESPError(conn net.Conn, s string) {
	_, _ = conn.Write([]byte("-" + s + "\r\n"))
}

func writeRESPBulkString(conn net.Conn, s string) {
	_, _ = conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(s), s)))
}

func collectByCheck(events []*types.Event) map[string]*types.Event {
	ret := make(map[string]*types.Event, len(events))
	for _, event := range events {
		ret[event.Labels["check"]] = event
	}
	return ret
}

func TestInitValidation(t *testing.T) {
	initTestConfig(t)

	tests := []struct {
		name    string
		ins     *Instance
		wantErr string
	}{
		{
			name: "invalid role",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				Role: RoleCheck{
					Expect: "sentinel",
				},
			},
			wantErr: "invalid role.expect",
		},
		{
			name: "bad response time threshold",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				ResponseTime: ResponseTimeCheck{
					WarnGe:     config.Duration(2 * time.Second),
					CriticalGe: config.Duration(time.Second),
				},
			},
			wantErr: "response_time.warn_ge",
		},
		{
			name: "negative db",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				DB:      -1,
			},
			wantErr: "db must be >= 0",
		},
		{
			name: "invalid master link status",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				MasterLink: MasterLinkCheck{
					Expect: "broken",
				},
			},
			wantErr: "invalid master_link_status.expect",
		},
		{
			name: "bad ops threshold",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				OpsPerSecond: OpsPerSecondCheck{
					WarnGe:     100,
					CriticalGe: 10,
				},
			},
			wantErr: "instantaneous_ops_per_sec.warn_ge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ins.Init()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestInitDefaultsAndNormalization(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		Targets: []string{"127.0.0.1"},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	if ins.Targets[0] != "127.0.0.1:6379" {
		t.Fatalf("expected default redis port, got %s", ins.Targets[0])
	}
	if ins.Concurrency != 10 {
		t.Fatalf("expected concurrency 10, got %d", ins.Concurrency)
	}
	if time.Duration(ins.Timeout) != 3*time.Second {
		t.Fatalf("expected timeout 3s, got %s", time.Duration(ins.Timeout))
	}
	if time.Duration(ins.ReadTimeout) != 2*time.Second {
		t.Fatalf("expected read_timeout 2s, got %s", time.Duration(ins.ReadTimeout))
	}
}

func TestGatherSuccess(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		password:         "secret",
		role:             "master",
		masterLinkStatus: "up",
		connectedSlaves:  2,
		connectedClients: 120,
		blockedClients:   3,
		rejectedConn:     12,
		evictedKeys:      100,
		expiredKeys:      200,
		opsPerSecond:     5000,
		usedMemory:       256 * 1024 * 1024,
		maxMemory:        512 * 1024 * 1024,
		loading:          0,
		rdbLastBgsave:    "ok",
		aofEnabled:       1,
		aofLastWrite:     "ok",
		pingDelay:        30 * time.Millisecond,
	})
	defer srv.Close()

	ins := &Instance{
		Targets:     []string{"redis.local:6379"},
		Password:    "secret",
		DB:          1,
		ReadTimeout: config.Duration(time.Second),
		ResponseTime: ResponseTimeCheck{
			WarnGe:     config.Duration(5 * time.Millisecond),
			CriticalGe: config.Duration(20 * time.Millisecond),
		},
		Role: RoleCheck{
			Expect:   "master",
			Severity: types.EventStatusWarning,
		},
		ConnectedClients: CountCheck{
			WarnGe:     100,
			CriticalGe: 200,
		},
		BlockedClients: CountCheck{
			WarnGe:     1,
			CriticalGe: 2,
		},
		UsedMemory: MemoryUsageCheck{
			WarnGe:     config.Size(128 * 1024 * 1024),
			CriticalGe: config.Size(512 * 1024 * 1024),
		},
		RejectedConn: CountCheck{
			WarnGe:     10,
			CriticalGe: 20,
		},
		ConnectedSlaves: MinCountCheck{
			WarnLt:     3,
			CriticalLt: 1,
		},
		EvictedKeys: CountCheck{
			WarnGe:     1,
			CriticalGe: 5,
		},
		ExpiredKeys: CountCheck{
			WarnGe:     1,
			CriticalGe: 5,
		},
		OpsPerSecond: OpsPerSecondCheck{
			WarnGe:     1000,
			CriticalGe: 10000,
		},
		Persistence: PersistenceCheck{
			Enabled:  true,
			Severity: types.EventStatusCritical,
		},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	_ = q.PopBackAll()

	srv.SetConfig(fakeRedisConfig{
		password:         "secret",
		role:             "master",
		masterLinkStatus: "up",
		connectedSlaves:  2,
		connectedClients: 120,
		blockedClients:   3,
		rejectedConn:     12,
		evictedKeys:      107,
		expiredKeys:      202,
		opsPerSecond:     5000,
		usedMemory:       256 * 1024 * 1024,
		maxMemory:        512 * 1024 * 1024,
		loading:          0,
		rdbLastBgsave:    "ok",
		aofEnabled:       1,
		aofLastWrite:     "ok",
		pingDelay:        30 * time.Millisecond,
	})
	q = safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) != 12 {
		t.Fatalf("expected 12 events, got %d", len(events))
	}

	byCheck := collectByCheck(events)

	if byCheck["redis::connectivity"].EventStatus != types.EventStatusOk {
		t.Fatalf("connectivity: expected Ok, got %s", byCheck["redis::connectivity"].EventStatus)
	}
	if byCheck["redis::response_time"].EventStatus != types.EventStatusCritical {
		t.Fatalf("response_time: expected Critical, got %s", byCheck["redis::response_time"].EventStatus)
	}
	if byCheck["redis::role"].EventStatus != types.EventStatusOk {
		t.Fatalf("role: expected Ok, got %s", byCheck["redis::role"].EventStatus)
	}
	if byCheck["redis::connected_clients"].EventStatus != types.EventStatusWarning {
		t.Fatalf("connected_clients: expected Warning, got %s", byCheck["redis::connected_clients"].EventStatus)
	}
	if byCheck["redis::blocked_clients"].EventStatus != types.EventStatusCritical {
		t.Fatalf("blocked_clients: expected Critical, got %s", byCheck["redis::blocked_clients"].EventStatus)
	}
	if byCheck["redis::used_memory"].EventStatus != types.EventStatusWarning {
		t.Fatalf("used_memory: expected Warning, got %s", byCheck["redis::used_memory"].EventStatus)
	}
	if byCheck["redis::rejected_connections"].EventStatus != types.EventStatusWarning {
		t.Fatalf("rejected_connections: expected Warning, got %s", byCheck["redis::rejected_connections"].EventStatus)
	}
	if byCheck["redis::connected_slaves"].EventStatus != types.EventStatusWarning {
		t.Fatalf("connected_slaves: expected Warning, got %s", byCheck["redis::connected_slaves"].EventStatus)
	}
	if byCheck["redis::evicted_keys"].EventStatus != types.EventStatusCritical {
		t.Fatalf("evicted_keys: expected Critical, got %s", byCheck["redis::evicted_keys"].EventStatus)
	}
	if byCheck["redis::expired_keys"].EventStatus != types.EventStatusWarning {
		t.Fatalf("expired_keys: expected Warning, got %s", byCheck["redis::expired_keys"].EventStatus)
	}
	if byCheck["redis::instantaneous_ops_per_sec"].EventStatus != types.EventStatusWarning {
		t.Fatalf("instantaneous_ops_per_sec: expected Warning, got %s", byCheck["redis::instantaneous_ops_per_sec"].EventStatus)
	}
	if byCheck["redis::persistence"].EventStatus != types.EventStatusOk {
		t.Fatalf("persistence: expected Ok, got %s", byCheck["redis::persistence"].EventStatus)
	}
}

func TestGatherAuthFailure(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		password: "secret",
		role:     "master",
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
		Password: "wrong",
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Labels["check"] != "redis::connectivity" {
		t.Fatalf("expected connectivity event, got %s", events[0].Labels["check"])
	}
	if events[0].EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical, got %s", events[0].EventStatus)
	}
	if !strings.Contains(events[0].Description, "WRONGPASS") {
		t.Fatalf("expected WRONGPASS in description, got %s", events[0].Description)
	}
}

func TestGatherRoleMismatch(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:             "slave",
		masterLinkStatus: "down",
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		Role: RoleCheck{
			Expect:   "master",
			Severity: types.EventStatusWarning,
		},
		MasterLink: MasterLinkCheck{
			Expect:   "up",
			Severity: types.EventStatusWarning,
		},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	byCheck := collectByCheck(events)
	if byCheck["redis::connectivity"].EventStatus != types.EventStatusOk {
		t.Fatalf("connectivity: expected Ok, got %s", byCheck["redis::connectivity"].EventStatus)
	}
	if byCheck["redis::role"].EventStatus != types.EventStatusWarning {
		t.Fatalf("role: expected Warning, got %s", byCheck["redis::role"].EventStatus)
	}
	if byCheck["redis::master_link_status"].EventStatus != types.EventStatusWarning {
		t.Fatalf("master_link_status: expected Warning, got %s", byCheck["redis::master_link_status"].EventStatus)
	}
}

func TestGatherPersistenceFailure(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:          "master",
		loading:       0,
		rdbLastBgsave: "err",
		aofEnabled:    1,
		aofLastWrite:  "ok",
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		Persistence: PersistenceCheck{
			Enabled:  true,
			Severity: types.EventStatusCritical,
		},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	byCheck := collectByCheck(events)
	if byCheck["redis::persistence"].EventStatus != types.EventStatusCritical {
		t.Fatalf("persistence: expected Critical, got %s", byCheck["redis::persistence"].EventStatus)
	}
}

func TestGatherDeltaCountersBaseline(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:        "master",
		evictedKeys: 10,
		expiredKeys: 20,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		EvictedKeys: CountCheck{
			WarnGe:     1,
			CriticalGe: 5,
		},
		ExpiredKeys: CountCheck{
			WarnGe:     1,
			CriticalGe: 5,
		},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	byCheck := collectByCheck(events)
	if byCheck["redis::evicted_keys"].EventStatus != types.EventStatusOk {
		t.Fatalf("evicted_keys baseline: expected Ok, got %s", byCheck["redis::evicted_keys"].EventStatus)
	}
	if byCheck["redis::expired_keys"].EventStatus != types.EventStatusOk {
		t.Fatalf("expired_keys baseline: expected Ok, got %s", byCheck["redis::expired_keys"].EventStatus)
	}
}

func TestInitTLSConfig(t *testing.T) {
	initTestConfig(t)

	ins := &Instance{
		Targets: []string{"redis.internal"},
		ClientConfig: tlscfg.ClientConfig{
			UseTLS: true,
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}
	if ins.tlsConfig == nil {
		t.Fatal("expected tls config to be initialized")
	}
	if _, ok := any(ins.tlsConfig).(*tls.Config); !ok {
		t.Fatal("expected stdlib tls.Config")
	}
}
