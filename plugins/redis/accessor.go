package redis

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// RedisAccessorConfig holds the connection parameters for creating a RedisAccessor.
type RedisAccessorConfig struct {
	Target      string
	Username    string
	Password    string
	DB          int
	Timeout     time.Duration
	ReadTimeout time.Duration
	TLSConfig   *tls.Config
	DialFunc    func(network, address string) (net.Conn, error)
}

// RedisAccessor encapsulates a Redis connection and provides structured data access.
// It handles connection, authentication, protocol interaction, and response parsing.
// Thread-unsafe: callers must synchronize concurrent use.
type RedisAccessor struct {
	client *redisClient
	target string
}

// NewRedisAccessor creates a connected and authenticated RedisAccessor.
func NewRedisAccessor(cfg RedisAccessorConfig) (*RedisAccessor, error) {
	var conn net.Conn
	var err error

	dialFn := cfg.DialFunc
	if dialFn == nil {
		dialer := &net.Dialer{Timeout: cfg.Timeout}
		dialFn = dialer.Dial
	}

	if cfg.TLSConfig != nil {
		tlsCfg := cfg.TLSConfig.Clone()
		host, _, splitErr := net.SplitHostPort(cfg.Target)
		if splitErr == nil && tlsCfg.ServerName == "" && net.ParseIP(host) == nil {
			tlsCfg.ServerName = host
		}
		rawConn, dialErr := dialFn("tcp", cfg.Target)
		if dialErr != nil {
			return nil, dialErr
		}
		tlsConn := tls.Client(rawConn, tlsCfg)
		if err := tlsConn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
			rawConn.Close()
			return nil, err
		}
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return nil, err
		}
		_ = tlsConn.SetDeadline(time.Time{})
		conn = tlsConn
	} else {
		conn, err = dialFn("tcp", cfg.Target)
		if err != nil {
			return nil, err
		}
	}

	client := &redisClient{
		conn:        conn,
		reader:      bufio.NewReader(conn),
		timeout:     cfg.Timeout,
		readTimeout: cfg.ReadTimeout,
	}

	if cfg.Password != "" || cfg.Username != "" {
		args := []string{"AUTH"}
		if cfg.Username != "" {
			args = append(args, cfg.Username)
		}
		args = append(args, cfg.Password)
		reply, err := client.command(args...)
		if err != nil {
			client.Close()
			return nil, err
		}
		if strings.ToUpper(reply) != "OK" {
			client.Close()
			return nil, fmt.Errorf("unexpected AUTH reply: %q", reply)
		}
	}

	if cfg.DB > 0 {
		reply, err := client.command("SELECT", strconv.Itoa(cfg.DB))
		if err != nil {
			client.Close()
			return nil, err
		}
		if strings.ToUpper(reply) != "OK" {
			client.Close()
			return nil, fmt.Errorf("unexpected SELECT reply: %q", reply)
		}
	}

	return &RedisAccessor{client: client, target: cfg.Target}, nil
}

// Close releases the underlying TCP connection.
func (a *RedisAccessor) Close() error {
	return a.client.Close()
}

// Ping sends PING and returns nil if the reply is PONG.
func (a *RedisAccessor) Ping() error {
	reply, err := a.client.command("PING")
	if err != nil {
		return err
	}
	if strings.ToUpper(reply) != "PONG" {
		return fmt.Errorf("unexpected PING reply: %q", reply)
	}
	return nil
}

// Info executes INFO <section> and returns the parsed key-value map.
func (a *RedisAccessor) Info(section string) (map[string]string, error) {
	raw, err := a.client.info(section)
	if err != nil {
		return nil, err
	}
	return parseInfoToMap(raw), nil
}

// Command executes an arbitrary Redis command and returns the string reply.
// For use by diagnostic tools that need flexible access.
func (a *RedisAccessor) Command(args ...string) (string, error) {
	return a.client.command(args...)
}

// Target returns the target address this accessor is connected to.
func (a *RedisAccessor) Target() string {
	return a.target
}

// InfoRaw executes INFO <section> and returns the raw string output.
func (a *RedisAccessor) InfoRaw(section string) (string, error) {
	return a.client.info(section)
}

// SlowlogGet executes SLOWLOG GET <count> and returns the raw response.
func (a *RedisAccessor) SlowlogGet(count int) (string, error) {
	if count <= 0 {
		count = 10
	}
	return a.client.command("SLOWLOG", "GET", strconv.Itoa(count))
}

// ClientList executes CLIENT LIST and returns the raw response.
func (a *RedisAccessor) ClientList() (string, error) {
	return a.client.command("CLIENT", "LIST")
}

// ConfigGet executes CONFIG GET <pattern> and returns the raw key-value pairs.
// Sensitive fields (passwords, auth keys) are redacted.
func (a *RedisAccessor) ConfigGet(pattern string) (string, error) {
	if pattern == "" {
		pattern = "*"
	}
	raw, err := a.client.command("CONFIG", "GET", pattern)
	if err != nil {
		return "", err
	}
	return filterSensitiveConfig(raw), nil
}

// DBSize executes DBSIZE and returns the reply.
func (a *RedisAccessor) DBSize() (string, error) {
	return a.client.command("DBSIZE")
}

var redisConfigDenyList = map[string]bool{
	"requirepass":       true,
	"masterauth":        true,
	"tls-key-file":      true,
	"tls-key-file-pass": true,
	"tls-cert-file":     true,
	"tls-ca-cert-file":  true,
}

func filterSensitiveConfig(raw string) string {
	lines := strings.Split(raw, "\n")
	var result []string
	redactNext := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if redactNext {
			result = append(result, "***REDACTED***")
			redactNext = false
			continue
		}
		if redisConfigDenyList[trimmed] {
			result = append(result, line)
			redactNext = true
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// --- Low-level RESP protocol client ---

type redisClient struct {
	conn        net.Conn
	reader      *bufio.Reader
	timeout     time.Duration
	readTimeout time.Duration
}

func (c *redisClient) Close() error {
	return c.conn.Close()
}

func (c *redisClient) info(section string) (string, error) {
	return c.command("INFO", section)
}

func (c *redisClient) command(args ...string) (string, error) {
	if err := c.writeCommand(args...); err != nil {
		return "", err
	}
	reply, err := c.readReply()
	if err != nil {
		return "", err
	}
	switch v := reply.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported redis reply type %T", reply)
	}
}

func (c *redisClient) writeCommand(args ...string) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, arg := range args {
		b.WriteString("$")
		b.WriteString(strconv.Itoa(len(arg)))
		b.WriteString("\r\n")
		b.WriteString(arg)
		b.WriteString("\r\n")
	}
	_, err := c.conn.Write([]byte(b.String()))
	return err
}

func (c *redisClient) readReply() (any, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
		return nil, err
	}
	prefix, err := c.reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		return line, nil
	case '-':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		return nil, errors.New(line)
	case ':':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, err
		}
		return strconv.FormatInt(n, 10), nil
	case '$':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		if size > maxBulkSize {
			return nil, fmt.Errorf("redis bulk string size %d exceeds limit %d", size, maxBulkSize)
		}
		buf := make([]byte, size+2)
		if _, err := ioReadFull(c.reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	default:
		return nil, fmt.Errorf("unsupported redis reply prefix %q", prefix)
	}
}

func (c *redisClient) readLine() (string, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

// --- Info parsing helpers ---

func parseInfoToMap(raw string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			fields[line[:idx]] = strings.TrimSpace(line[idx+1:])
		}
	}
	return fields
}

func infoGetInt(info map[string]string, key string) (int, bool, error) {
	value, ok := info[key]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func infoGetInt64(info map[string]string, key string) (int64, bool, error) {
	value, ok := info[key]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func infoGetUint64(info map[string]string, key string) (uint64, bool, error) {
	value, ok := info[key]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return n, true, nil
}
