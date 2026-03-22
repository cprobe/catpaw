package redis

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	clogger "github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	tlscfg "github.com/cprobe/catpaw/digcore/pkg/tls"
	"github.com/cprobe/catpaw/digcore/types"
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
	username             string
	password             string
	configGet            map[string]string
	infoErrors           map[string]string
	redisMode            string
	role                 string
	masterReplOffset     int64
	slaveReplOffset      int64
	replicaOffsets       []int64
	masterLinkStatus     string
	masterHost           string
	masterPort           string
	connectedSlaves      int
	connectedClients     int
	blockedClients       int
	rejectedConn         int
	evictedKeys          uint64
	expiredKeys          uint64
	opsPerSecond         int
	usedMemory           int64
	maxMemory            int64
	loading              int
	rdbLastBgsave        string
	rdbInProgress        int
	aofEnabled           int
	aofLastWrite         string
	aofRewrite           int
	clusterState         string
	clusterSlotsAssigned int
	clusterSlotsFail     int
	clusterKnownNodes    int
	clusterNodes         string
	keys                 map[string]fakeRedisKey
	pingDelay            time.Duration
}

type fakeRedisKey struct {
	Type string
	Size int64
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
			if msg, ok := cfg.infoErrors[section]; ok {
				writeRESPError(conn, msg)
				continue
			}
			redisMode := cfg.redisMode
			if redisMode == "" {
				redisMode = redisModeStandalone
			}
			switch section {
			case "server":
				writeRESPBulkString(conn, fmt.Sprintf("# Server\r\nredis_mode:%s\r\n", redisMode))
			case "replication":
				replication := fmt.Sprintf("# Replication\r\nrole:%s\r\nmaster_link_status:%s\r\nmaster_host:%s\r\nmaster_port:%s\r\nconnected_slaves:%d\r\n",
					cfg.role, cfg.masterLinkStatus, cfg.masterHost, cfg.masterPort, cfg.connectedSlaves)
				if cfg.role == "master" && cfg.masterReplOffset > 0 {
					replication += fmt.Sprintf("master_repl_offset:%d\r\n", cfg.masterReplOffset)
					for i, offset := range cfg.replicaOffsets {
						replication += fmt.Sprintf("slave%d:ip=10.0.0.%d,port=6379,state=online,offset=%d,lag=1\r\n", i, i+10, offset)
					}
				}
				if (cfg.role == "slave" || cfg.role == "replica") && (cfg.masterReplOffset > 0 || cfg.slaveReplOffset > 0) {
					replication += fmt.Sprintf("master_repl_offset:%d\r\nslave_repl_offset:%d\r\n", cfg.masterReplOffset, cfg.slaveReplOffset)
				}
				writeRESPBulkString(conn, replication)
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
		case "CLUSTER":
			if cfg.redisMode != redisModeCluster {
				writeRESPError(conn, "ERR This instance has cluster support disabled")
				continue
			}
			if len(args) < 2 {
				writeRESPError(conn, "ERR wrong number of arguments for 'cluster' command")
				continue
			}
			switch strings.ToUpper(args[1]) {
			case "INFO":
				clusterState := cfg.clusterState
				if clusterState == "" {
					clusterState = "ok"
				}
				slotsAssigned := cfg.clusterSlotsAssigned
				if slotsAssigned == 0 {
					slotsAssigned = clusterSlotsFull
				}
				clusterKnownNodes := cfg.clusterKnownNodes
				if clusterKnownNodes == 0 {
					clusterKnownNodes = 6
				}
				writeRESPBulkString(conn, fmt.Sprintf("# Cluster\r\ncluster_state:%s\r\ncluster_slots_assigned:%d\r\ncluster_slots_ok:%d\r\ncluster_slots_fail:%d\r\ncluster_known_nodes:%d\r\ncluster_size:3\r\n",
					clusterState, slotsAssigned, slotsAssigned-cfg.clusterSlotsFail, cfg.clusterSlotsFail, clusterKnownNodes))
			case "NODES":
				clusterNodes := cfg.clusterNodes
				if clusterNodes == "" {
					clusterNodes = strings.Join([]string{
						"07c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1aa 10.0.0.10:6379@16379 master - 0 0 1 connected 0-5460",
						"18c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1ab 10.0.0.11:6379@16379 master - 0 0 2 connected 5461-10922",
						"29c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1ac 10.0.0.12:6379@16379 master - 0 0 3 connected 10923-16383",
					}, "\n")
				}
				writeRESPBulkString(conn, clusterNodes)
			default:
				writeRESPError(conn, "ERR unknown subcommand for CLUSTER")
			}
		case "CONFIG":
			if len(args) < 3 || strings.ToUpper(args[1]) != "GET" {
				writeRESPError(conn, "ERR unsupported CONFIG command")
				continue
			}
			pattern := args[2]
			if pattern == "" {
				pattern = "*"
			}
			keys := make([]string, 0, len(cfg.configGet))
			for key := range cfg.configGet {
				ok, err := path.Match(pattern, key)
				if err == nil && ok {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			reply := make([]any, 0, len(keys)*2)
			for _, key := range keys {
				reply = append(reply, key, cfg.configGet[key])
			}
			writeRESPValue(conn, reply)
		case "SCAN":
			cursor := "0"
			if len(args) > 1 {
				cursor = args[1]
			}
			match := "*"
			count := 10
			for i := 2; i+1 < len(args); i += 2 {
				switch strings.ToUpper(args[i]) {
				case "MATCH":
					match = args[i+1]
				case "COUNT":
					if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
						count = n
					}
				}
			}
			var keys []string
			for key := range cfg.keys {
				ok, err := path.Match(match, key)
				if err == nil && ok {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			start, _ := strconv.Atoi(cursor)
			if start < 0 {
				start = 0
			}
			end := start + count
			if end > len(keys) {
				end = len(keys)
			}
			nextCursor := "0"
			if end < len(keys) {
				nextCursor = strconv.Itoa(end)
			}
			reply := []any{nextCursor, make([]any, 0, end-start)}
			for _, key := range keys[start:end] {
				reply[1] = append(reply[1].([]any), key)
			}
			writeRESPValue(conn, reply)
		case "TYPE":
			if len(args) < 2 {
				writeRESPError(conn, "ERR wrong number of arguments for 'type' command")
				continue
			}
			if key, ok := cfg.keys[args[1]]; ok {
				writeRESPSimpleString(conn, key.Type)
			} else {
				writeRESPSimpleString(conn, "none")
			}
		case "MEMORY":
			if len(args) < 3 || strings.ToUpper(args[1]) != "USAGE" {
				writeRESPError(conn, "ERR unsupported MEMORY command")
				continue
			}
			key, ok := cfg.keys[args[2]]
			if !ok {
				writeRESPNull(conn)
				continue
			}
			writeRESPInteger(conn, key.Size)
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

func writeRESPNull(conn net.Conn) {
	_, _ = conn.Write([]byte("$-1\r\n"))
}

func writeRESPInteger(conn net.Conn, n int64) {
	_, _ = conn.Write([]byte(fmt.Sprintf(":%d\r\n", n)))
}

func writeRESPArray(conn net.Conn, arr []any) {
	_, _ = conn.Write([]byte(fmt.Sprintf("*%d\r\n", len(arr))))
	for _, item := range arr {
		writeRESPValue(conn, item)
	}
}

func writeRESPValue(conn net.Conn, v any) {
	switch val := v.(type) {
	case string:
		writeRESPBulkString(conn, val)
	case []any:
		writeRESPArray(conn, val)
	case int:
		writeRESPInteger(conn, int64(val))
	case int64:
		writeRESPInteger(conn, val)
	default:
		writeRESPBulkString(conn, fmt.Sprintf("%v", val))
	}
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
		{
			name: "bad repl lag threshold",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				ReplLag: ReplLagCheck{
					WarnGe:     config.Size(10 * 1024 * 1024),
					CriticalGe: config.Size(1024 * 1024),
				},
			},
			wantErr: "repl_lag.warn_ge",
		},
		{
			name: "bad used memory pct threshold",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				UsedMemoryPct: PercentCheck{
					WarnGe:     95,
					CriticalGe: 80,
				},
			},
			wantErr: "used_memory_pct.warn_ge",
		},
		{
			name: "used memory pct too large",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				UsedMemoryPct: PercentCheck{
					CriticalGe: 101,
				},
			},
			wantErr: "used_memory_pct thresholds must be <= 100",
		},
		{
			name: "invalid mode",
			ins: &Instance{
				Targets: []string{"127.0.0.1"},
				Mode:    "sentinel",
			},
			wantErr: "invalid mode",
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
	boolPtr := func(v bool) *bool { return &v }

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
			Enabled:  boolPtr(true),
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
		rejectedConn:     22,
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

func TestGatherClusterHealthy(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		redisMode:            redisModeCluster,
		clusterState:         "ok",
		clusterSlotsAssigned: clusterSlotsFull,
		clusterSlotsFail:     0,
		clusterKnownNodes:    6,
	})
	defer srv.Close()

	ins := &Instance{
		Targets:     []string{"redis.local:6379"},
		ClusterName: "prod-cache",
		dialFunc:    srv.Dial,
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
	if byCheck["redis::cluster_state"].EventStatus != types.EventStatusOk {
		t.Fatalf("cluster_state: expected Ok, got %s", byCheck["redis::cluster_state"].EventStatus)
	}
	if byCheck["redis::cluster_topology"].EventStatus != types.EventStatusOk {
		t.Fatalf("cluster_topology: expected Ok, got %s", byCheck["redis::cluster_topology"].EventStatus)
	}
	if byCheck["redis::cluster_state"].Labels["cluster_name"] != "prod-cache" {
		t.Fatalf("expected cluster_name label, got %q", byCheck["redis::cluster_state"].Labels["cluster_name"])
	}
}

func TestGatherClusterFailure(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		redisMode:            redisModeCluster,
		clusterState:         "fail",
		clusterSlotsAssigned: 12000,
		clusterSlotsFail:     128,
		clusterNodes: strings.Join([]string{
			"07c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1aa 10.0.0.10:6379@16379 master,fail - 0 0 1 connected 0-5460",
			"18c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1ab 10.0.0.11:6379@16379 master - 0 0 2 connected 5461-10922",
			"29c37dfeb2352e0b2f91c9f3f2f7f3f7a4f9c1ac 10.0.0.12:6379@16379 master - 0 0 3 connected 10923-12000",
		}, "\n"),
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
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
	if byCheck["redis::cluster_state"].EventStatus != types.EventStatusCritical {
		t.Fatalf("cluster_state: expected Critical, got %s", byCheck["redis::cluster_state"].EventStatus)
	}
	if byCheck["redis::cluster_topology"].EventStatus != types.EventStatusCritical {
		t.Fatalf("cluster_topology: expected Critical, got %s", byCheck["redis::cluster_topology"].EventStatus)
	}
	if !strings.Contains(byCheck["redis::cluster_topology"].Description, "fail node") {
		t.Fatalf("expected fail node detail, got %q", byCheck["redis::cluster_topology"].Description)
	}
}

func TestGatherClusterModeMismatch(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		redisMode: redisModeStandalone,
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
		Mode:     redisModeCluster,
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
	if byCheck["redis::cluster_state"].EventStatus != types.EventStatusCritical {
		t.Fatalf("cluster_state: expected Critical, got %s", byCheck["redis::cluster_state"].EventStatus)
	}
	if byCheck["redis::cluster_topology"].EventStatus != types.EventStatusCritical {
		t.Fatalf("cluster_topology: expected Critical, got %s", byCheck["redis::cluster_topology"].EventStatus)
	}
}

func TestGatherReplicaReplLag(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:             "slave",
		masterLinkStatus: "up",
		masterReplOffset: 10 * 1024 * 1024,
		slaveReplOffset:  8 * 1024 * 1024,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		ReplLag: ReplLagCheck{
			WarnGe:     config.Size(1024 * 1024),
			CriticalGe: config.Size(5 * 1024 * 1024),
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
	if byCheck["redis::repl_lag"].EventStatus != types.EventStatusWarning {
		t.Fatalf("repl_lag: expected Warning, got %s", byCheck["redis::repl_lag"].EventStatus)
	}
}

func TestGatherMasterReplLag(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:             "master",
		connectedSlaves:  2,
		masterReplOffset: 10 * 1024 * 1024,
		replicaOffsets: []int64{
			9 * 1024 * 1024,
			4 * 1024 * 1024,
		},
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		ReplLag: ReplLagCheck{
			WarnGe:     config.Size(1024 * 1024),
			CriticalGe: config.Size(5 * 1024 * 1024),
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
	if byCheck["redis::repl_lag"].EventStatus != types.EventStatusCritical {
		t.Fatalf("repl_lag: expected Critical, got %s", byCheck["redis::repl_lag"].EventStatus)
	}
}

func TestGatherUsedMemoryPct(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:       "master",
		usedMemory: 90 * 1024 * 1024,
		maxMemory:  100 * 1024 * 1024,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		UsedMemoryPct: PercentCheck{
			WarnGe:     80,
			CriticalGe: 95,
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
	if byCheck["redis::used_memory_pct"].EventStatus != types.EventStatusWarning {
		t.Fatalf("used_memory_pct: expected Warning, got %s", byCheck["redis::used_memory_pct"].EventStatus)
	}
}

func TestGatherUsedMemoryPctUnlimitedMaxmemory(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:       "master",
		usedMemory: 90 * 1024 * 1024,
		maxMemory:  0,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		UsedMemoryPct: PercentCheck{
			WarnGe:     80,
			CriticalGe: 95,
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
	if byCheck["redis::used_memory_pct"].EventStatus != types.EventStatusOk {
		t.Fatalf("used_memory_pct: expected Ok, got %s", byCheck["redis::used_memory_pct"].EventStatus)
	}
	if !strings.Contains(byCheck["redis::used_memory_pct"].Description, "skipped") {
		t.Fatalf("expected skip description, got %q", byCheck["redis::used_memory_pct"].Description)
	}
}

func TestGatherPersistenceFailure(t *testing.T) {
	initTestConfig(t)
	boolPtr := func(v bool) *bool { return &v }

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
			Enabled:  boolPtr(true),
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

	boolPtr := func(v bool) *bool { return &v }
	ins := &Instance{
		Targets: []string{"redis.internal"},
		ClientConfig: tlscfg.ClientConfig{
			UseTLS: boolPtr(true),
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

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "host only", input: "redis.local", want: "redis.local:6379"},
		{name: "host:port", input: "redis.local:6380", want: "redis.local:6380"},
		{name: "ip only", input: "10.0.0.1", want: "10.0.0.1:6379"},
		{name: "ip:port", input: "10.0.0.1:6380", want: "10.0.0.1:6380"},
		{name: "localhost", input: "localhost", want: "localhost:6379"},
		{name: "ipv6 bracket", input: "[::1]:6379", want: "[::1]:6379"},
		{name: "empty host with port", input: ":6379", want: "localhost:6379"},
		{name: "empty", input: "", wantErr: "must not be empty"},
		{name: "whitespace only", input: "   ", wantErr: "must not be empty"},
		{name: "ipv6 no bracket", input: "::1", wantErr: "failed to parse redis target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTarget(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestApplyPartials(t *testing.T) {
	initTestConfig(t)

	boolPtr := func(v bool) *bool { return &v }

	plugin := &RedisPlugin{
		Partials: []Partial{
			{
				ID:          "base",
				Concurrency: 5,
				Timeout:     config.Duration(5 * time.Second),
				ReadTimeout: config.Duration(3 * time.Second),
				Password:    "shared-pass",
				Connectivity: ConnectivityCheck{
					Severity: types.EventStatusWarning,
				},
				ConnectedClients: CountCheck{
					WarnGe:     100,
					CriticalGe: 500,
				},
				UsedMemory: MemoryUsageCheck{
					WarnGe:     config.Size(256 * 1024 * 1024),
					CriticalGe: config.Size(1024 * 1024 * 1024),
				},
				Role: RoleCheck{
					Expect:   "master",
					Severity: types.EventStatusWarning,
				},
				ClientConfig: tlscfg.ClientConfig{
					UseTLS:             boolPtr(true),
					InsecureSkipVerify: boolPtr(true),
				},
				Persistence: PersistenceCheck{
					Enabled: boolPtr(true),
				},
				ClusterState: ClusterStateCheck{
					Disabled: boolPtr(true),
				},
				ClusterTopology: ClusterTopologyCheck{
					Disabled: boolPtr(true),
				},
			},
		},
		Instances: []*Instance{
			{
				Partial: "base",
				Targets: []string{"redis.local:6379"},
				ConnectedClients: CountCheck{
					WarnGe: 200,
				},
				ClientConfig: tlscfg.ClientConfig{
					UseTLS:             boolPtr(false),
					InsecureSkipVerify: boolPtr(false),
				},
				Persistence: PersistenceCheck{
					Enabled: boolPtr(false),
				},
				ClusterState: ClusterStateCheck{
					Disabled: boolPtr(false),
				},
				ClusterTopology: ClusterTopologyCheck{
					Disabled: boolPtr(false),
				},
			},
			{
				Partial:  "base",
				Targets:  []string{"redis2.local:6379"},
				Password: "override-pass",
			},
			{
				Targets: []string{"redis3.local:6379"},
			},
		},
	}

	if err := plugin.ApplyPartials(); err != nil {
		t.Fatal(err)
	}

	ins0 := plugin.Instances[0]
	if ins0.Concurrency != 5 {
		t.Fatalf("ins0 concurrency: expected 5, got %d", ins0.Concurrency)
	}
	if ins0.Password != "shared-pass" {
		t.Fatalf("ins0 password: expected shared-pass, got %s", ins0.Password)
	}
	if ins0.ConnectedClients.WarnGe != 200 {
		t.Fatalf("ins0 connected_clients.warn_ge: expected 200 (not overwritten), got %d", ins0.ConnectedClients.WarnGe)
	}
	if ins0.ConnectedClients.CriticalGe != 500 {
		t.Fatalf("ins0 connected_clients.critical_ge: expected 500 (from partial), got %d", ins0.ConnectedClients.CriticalGe)
	}
	if ins0.Role.Expect != "master" {
		t.Fatalf("ins0 role.expect: expected master, got %s", ins0.Role.Expect)
	}
	if ins0.Connectivity.Severity != types.EventStatusWarning {
		t.Fatalf("ins0 connectivity.severity: expected Warning, got %s", ins0.Connectivity.Severity)
	}
	if ins0.ClientConfig.UseTLS == nil || *ins0.ClientConfig.UseTLS {
		t.Fatalf("ins0 use_tls: expected explicit false to be preserved, got %+v", ins0.ClientConfig.UseTLS)
	}
	if ins0.ClientConfig.InsecureSkipVerify == nil || *ins0.ClientConfig.InsecureSkipVerify {
		t.Fatalf("ins0 insecure_skip_verify: expected explicit false to be preserved, got %+v", ins0.ClientConfig.InsecureSkipVerify)
	}
	if ins0.Persistence.Enabled == nil || *ins0.Persistence.Enabled {
		t.Fatalf("ins0 persistence.enabled: expected explicit false to be preserved, got %+v", ins0.Persistence.Enabled)
	}
	if ins0.ClusterState.Disabled == nil || *ins0.ClusterState.Disabled {
		t.Fatalf("ins0 cluster_state.disabled: expected explicit false to be preserved, got %+v", ins0.ClusterState.Disabled)
	}
	if ins0.ClusterTopology.Disabled == nil || *ins0.ClusterTopology.Disabled {
		t.Fatalf("ins0 cluster_topology.disabled: expected explicit false to be preserved, got %+v", ins0.ClusterTopology.Disabled)
	}

	ins1 := plugin.Instances[1]
	if ins1.Password != "override-pass" {
		t.Fatalf("ins1 password: expected override-pass (not overwritten), got %s", ins1.Password)
	}
	if ins1.Concurrency != 5 {
		t.Fatalf("ins1 concurrency: expected 5, got %d", ins1.Concurrency)
	}
	if ins1.ClientConfig.UseTLS == nil || !*ins1.ClientConfig.UseTLS {
		t.Fatalf("ins1 use_tls: expected partial true to apply, got %+v", ins1.ClientConfig.UseTLS)
	}
	if ins1.ClientConfig.InsecureSkipVerify == nil || !*ins1.ClientConfig.InsecureSkipVerify {
		t.Fatalf("ins1 insecure_skip_verify: expected partial true to apply, got %+v", ins1.ClientConfig.InsecureSkipVerify)
	}
	if ins1.Persistence.Enabled == nil || !*ins1.Persistence.Enabled {
		t.Fatalf("ins1 persistence.enabled: expected partial true to apply, got %+v", ins1.Persistence.Enabled)
	}
	if ins1.ClusterState.Disabled == nil || !*ins1.ClusterState.Disabled {
		t.Fatalf("ins1 cluster_state.disabled: expected partial true to apply, got %+v", ins1.ClusterState.Disabled)
	}
	if ins1.ClusterTopology.Disabled == nil || !*ins1.ClusterTopology.Disabled {
		t.Fatalf("ins1 cluster_topology.disabled: expected partial true to apply, got %+v", ins1.ClusterTopology.Disabled)
	}

	ins2 := plugin.Instances[2]
	if ins2.Concurrency != 0 {
		t.Fatalf("ins2 concurrency: expected 0 (no partial), got %d", ins2.Concurrency)
	}
	if ins2.Password != "" {
		t.Fatalf("ins2 password: expected empty (no partial), got %s", ins2.Password)
	}
}

func TestApplyPartialsMissing(t *testing.T) {
	initTestConfig(t)

	plugin := &RedisPlugin{
		Instances: []*Instance{
			{
				Partial: "missing",
				Targets: []string{"redis.local:6379"},
			},
		},
	}

	err := plugin.ApplyPartials()
	if err == nil {
		t.Fatal("expected missing partial error")
	}
	if !strings.Contains(err.Error(), `partial "missing" not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigGetRedactsStructuredReply(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		configGet: map[string]string{
			"maxmemory":   "104857600",
			"requirepass": "super-secret",
			"masterauth":  "upstream-secret",
		},
	})
	defer srv.Close()

	acc, err := NewRedisAccessor(RedisAccessorConfig{
		Target:      "redis.local:6379",
		Timeout:     time.Second,
		ReadTimeout: time.Second,
		DialFunc:    srv.Dial,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()

	out, err := acc.ConfigGet("*")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "super-secret") || strings.Contains(out, "upstream-secret") {
		t.Fatalf("sensitive config should be redacted, got:\n%s", out)
	}
	if strings.Count(out, "***REDACTED***") != 2 {
		t.Fatalf("expected 2 redactions, got %d:\n%s", strings.Count(out, "***REDACTED***"), out)
	}
	if !strings.Contains(out, "maxmemory") || !strings.Contains(out, "104857600") {
		t.Fatalf("non-sensitive config should be preserved, got:\n%s", out)
	}
}

func TestParseInfoToMap(t *testing.T) {
	raw := "# Server\r\nredis_version:7.0.0\r\nuptime_in_seconds:12345\r\n\r\n# Clients\r\nconnected_clients:42\r\n"
	m := parseInfoToMap(raw)

	if v, ok := m["redis_version"]; !ok || v != "7.0.0" {
		t.Fatalf("expected redis_version=7.0.0, got %q ok=%v", v, ok)
	}
	if v, ok := m["uptime_in_seconds"]; !ok || v != "12345" {
		t.Fatalf("expected uptime_in_seconds=12345, got %q ok=%v", v, ok)
	}
	if v, ok := m["connected_clients"]; !ok || v != "42" {
		t.Fatalf("expected connected_clients=42, got %q ok=%v", v, ok)
	}
	if _, ok := m["# Server"]; ok {
		t.Fatal("comment lines should not be in map")
	}
}

func TestGatherRejectedConnectionsDelta(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		role:         "master",
		rejectedConn: 100,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		RejectedConn: CountCheck{
			WarnGe:     5,
			CriticalGe: 20,
		},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	byCheck := collectByCheck(events)
	if byCheck["redis::rejected_connections"].EventStatus != types.EventStatusOk {
		t.Fatalf("rejected_connections baseline: expected Ok, got %s", byCheck["redis::rejected_connections"].EventStatus)
	}

	srv.SetConfig(fakeRedisConfig{
		role:         "master",
		rejectedConn: 110,
	})
	q = safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events = q.PopBackAll()
	byCheck = collectByCheck(events)
	if byCheck["redis::rejected_connections"].EventStatus != types.EventStatusWarning {
		t.Fatalf("rejected_connections delta 10: expected Warning, got %s", byCheck["redis::rejected_connections"].EventStatus)
	}

	srv.SetConfig(fakeRedisConfig{
		role:         "master",
		rejectedConn: 135,
	})
	q = safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events = q.PopBackAll()
	byCheck = collectByCheck(events)
	if byCheck["redis::rejected_connections"].EventStatus != types.EventStatusCritical {
		t.Fatalf("rejected_connections delta 25: expected Critical, got %s", byCheck["redis::rejected_connections"].EventStatus)
	}
}

func TestGatherClientsErrorDoesNotBlockOtherChecks(t *testing.T) {
	initTestConfig(t)
	boolPtr := func(v bool) *bool { return &v }

	srv := startFakeRedisServer(t, fakeRedisConfig{
		infoErrors: map[string]string{
			"clients": "ERR clients section unavailable",
		},
		role:          "master",
		loading:       0,
		rdbLastBgsave: "ok",
		aofEnabled:    0,
		usedMemory:    100 * 1024 * 1024,
		maxMemory:     512 * 1024 * 1024,
	})
	defer srv.Close()

	ins := &Instance{
		Targets: []string{"redis.local:6379"},
		ConnectedClients: CountCheck{
			WarnGe:     100,
			CriticalGe: 200,
		},
		UsedMemory: MemoryUsageCheck{
			WarnGe:     config.Size(512 * 1024 * 1024),
			CriticalGe: config.Size(1024 * 1024 * 1024),
		},
		Persistence: PersistenceCheck{
			Enabled:  boolPtr(true),
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

	byCheck := collectByCheck(events)
	if _, ok := byCheck["redis::connectivity"]; !ok {
		t.Fatal("expected connectivity event")
	}
	if _, ok := byCheck["redis::connected_clients"]; !ok {
		t.Fatal("expected connected_clients event even if clients info may fail")
	}
	if _, ok := byCheck["redis::used_memory"]; !ok {
		t.Fatal("expected used_memory event (should not be blocked by clients check)")
	}
	if _, ok := byCheck["redis::persistence"]; !ok {
		t.Fatal("expected persistence event (should not be blocked by clients check)")
	}
}

func TestGatherAutoModeServerInfoFailureDoesNotEmitClusterEvents(t *testing.T) {
	initTestConfig(t)

	srv := startFakeRedisServer(t, fakeRedisConfig{
		infoErrors: map[string]string{
			"server": "ERR server section unavailable",
		},
		role: "master",
	})
	defer srv.Close()

	ins := &Instance{
		Targets:  []string{"redis.local:6379"},
		dialFunc: srv.Dial,
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	events := q.PopBackAll()
	byCheck := collectByCheck(events)

	if _, ok := byCheck["redis::cluster_state"]; ok {
		t.Fatal("did not expect cluster_state event in auto mode when INFO server fails")
	}
	if _, ok := byCheck["redis::cluster_topology"]; ok {
		t.Fatal("did not expect cluster_topology event in auto mode when INFO server fails")
	}
	if byCheck["redis::connectivity"].EventStatus != types.EventStatusOk {
		t.Fatalf("connectivity: expected Ok, got %s", byCheck["redis::connectivity"].EventStatus)
	}
}
