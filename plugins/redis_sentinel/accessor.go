package redis_sentinel

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SentinelAccessorConfig struct {
	Target      string
	Username    string
	Password    string
	Timeout     time.Duration
	ReadTimeout time.Duration
	TLSConfig   *tls.Config
	DialFunc    func(network, address string) (net.Conn, error)
}

type SentinelAccessor struct {
	client *sentinelClient
	target string
}

type SentinelMasterInfo map[string]string

func NewSentinelAccessor(cfg SentinelAccessorConfig) (*SentinelAccessor, error) {
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

	client := &sentinelClient{
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

	return &SentinelAccessor{client: client, target: cfg.Target}, nil
}

func (a *SentinelAccessor) Close() error { return a.client.Close() }

func (a *SentinelAccessor) Target() string { return a.target }

func (a *SentinelAccessor) Ping() error {
	reply, err := a.client.command("PING")
	if err != nil {
		return err
	}
	if strings.ToUpper(reply) != "PONG" {
		return fmt.Errorf("unexpected PING reply: %q", reply)
	}
	return nil
}

func (a *SentinelAccessor) Role() (string, error) {
	reply, err := a.client.rawCommand("ROLE")
	if err != nil {
		return "", err
	}
	arr, ok := reply.([]any)
	if !ok || len(arr) == 0 {
		return "", fmt.Errorf("unexpected ROLE reply type %T", reply)
	}
	role, ok := arr[0].(string)
	if !ok {
		return "", fmt.Errorf("unexpected ROLE reply element type %T", arr[0])
	}
	return strings.ToLower(strings.TrimSpace(role)), nil
}

func (a *SentinelAccessor) Info(section string) (map[string]string, error) {
	raw, err := a.client.info(section)
	if err != nil {
		return nil, err
	}
	return parseInfoToMap(raw), nil
}

func (a *SentinelAccessor) InfoRaw(section string) (string, error) {
	return a.client.info(section)
}

func (a *SentinelAccessor) SentinelMasters() ([]SentinelMasterInfo, error) {
	reply, err := a.client.rawCommand("SENTINEL", "MASTERS")
	if err != nil {
		return nil, err
	}
	return parseListOfStringMaps(reply)
}

func (a *SentinelAccessor) SentinelMaster(name string) (SentinelMasterInfo, error) {
	reply, err := a.client.rawCommand("SENTINEL", "MASTER", name)
	if err != nil {
		return nil, err
	}
	return parseStringMap(reply)
}

func (a *SentinelAccessor) SentinelReplicas(name string) ([]SentinelMasterInfo, error) {
	reply, err := a.client.rawCommand("SENTINEL", "REPLICAS", name)
	if err != nil {
		return nil, err
	}
	return parseListOfStringMaps(reply)
}

func (a *SentinelAccessor) SentinelSentinels(name string) ([]SentinelMasterInfo, error) {
	reply, err := a.client.rawCommand("SENTINEL", "SENTINELS", name)
	if err != nil {
		return nil, err
	}
	return parseListOfStringMaps(reply)
}

func (a *SentinelAccessor) SentinelCKQuorum(name string) (string, error) {
	return a.client.command("SENTINEL", "CKQUORUM", name)
}

func (a *SentinelAccessor) SentinelGetMasterAddrByName(name string) (string, error) {
	reply, err := a.client.rawCommand("SENTINEL", "GET-MASTER-ADDR-BY-NAME", name)
	if err != nil {
		return "", err
	}
	arr, ok := reply.([]any)
	if !ok {
		return "", fmt.Errorf("unexpected GET-MASTER-ADDR-BY-NAME reply type %T", reply)
	}
	if len(arr) != 2 {
		return "", fmt.Errorf("unexpected GET-MASTER-ADDR-BY-NAME reply length %d", len(arr))
	}
	host, ok := arr[0].(string)
	if !ok {
		return "", fmt.Errorf("unexpected GET-MASTER-ADDR-BY-NAME host type %T", arr[0])
	}
	port, ok := arr[1].(string)
	if !ok {
		return "", fmt.Errorf("unexpected GET-MASTER-ADDR-BY-NAME port type %T", arr[1])
	}
	return net.JoinHostPort(host, port), nil
}

func (a *SentinelAccessor) Command(args ...string) (string, error) {
	return a.client.command(args...)
}

type sentinelClient struct {
	conn        net.Conn
	reader      *bufio.Reader
	timeout     time.Duration
	readTimeout time.Duration
}

func (c *sentinelClient) Close() error { return c.conn.Close() }

func (c *sentinelClient) info(section string) (string, error) {
	if section == "" {
		return c.command("INFO")
	}
	return c.command("INFO", section)
}

func (c *sentinelClient) command(args ...string) (string, error) {
	reply, err := c.rawCommand(args...)
	if err != nil {
		return "", err
	}
	return formatReply(reply), nil
}

func (c *sentinelClient) rawCommand(args ...string) (any, error) {
	if err := c.writeCommand(args...); err != nil {
		return nil, err
	}
	return c.readReply()
}

func (c *sentinelClient) writeCommand(args ...string) error {
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

func (c *sentinelClient) readReply() (any, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
		return nil, err
	}
	return c.readReplyValue()
}

func (c *sentinelClient) readReplyValue() (any, error) {
	prefix, err := c.reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		return c.readLine()
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
			return nil, fmt.Errorf("redis_sentinel bulk string size %d exceeds limit %d", size, maxBulkSize)
		}
		buf := make([]byte, size+2)
		if _, err := ioReadFull(c.reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	case '*':
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		count, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if count < 0 {
			return nil, nil
		}
		elems := make([]any, count)
		for i := 0; i < count; i++ {
			elems[i], err = c.readReplyValue()
			if err != nil {
				return nil, err
			}
		}
		return elems, nil
	default:
		return nil, fmt.Errorf("unsupported redis_sentinel reply prefix %q", prefix)
	}
}

func (c *sentinelClient) readLine() (string, error) {
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

func formatReply(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case nil:
		return "(nil)"
	case []any:
		var b strings.Builder
		formatArray(&b, val, 0)
		return strings.TrimRight(b.String(), "\n")
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatArray(b *strings.Builder, arr []any, depth int) {
	indent := strings.Repeat("  ", depth)
	for i, elem := range arr {
		switch v := elem.(type) {
		case []any:
			fmt.Fprintf(b, "%s%d)\n", indent, i+1)
			formatArray(b, v, depth+1)
		case string:
			fmt.Fprintf(b, "%s%d) %s\n", indent, i+1, v)
		case nil:
			fmt.Fprintf(b, "%s%d) (nil)\n", indent, i+1)
		default:
			fmt.Fprintf(b, "%s%d) %v\n", indent, i+1, v)
		}
	}
}

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

func parseListOfStringMaps(reply any) ([]SentinelMasterInfo, error) {
	arr, ok := reply.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected reply type %T", reply)
	}
	result := make([]SentinelMasterInfo, 0, len(arr))
	for _, item := range arr {
		parsed, err := parseStringMap(item)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i]["name"] < result[j]["name"]
	})
	return result, nil
}

func parseStringMap(reply any) (SentinelMasterInfo, error) {
	arr, ok := reply.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected reply type %T", reply)
	}
	if len(arr)%2 != 0 {
		return nil, fmt.Errorf("unexpected alternating key/value length %d", len(arr))
	}
	result := make(SentinelMasterInfo, len(arr)/2)
	for i := 0; i < len(arr); i += 2 {
		key, ok := arr[i].(string)
		if !ok {
			return nil, fmt.Errorf("unexpected key type %T", arr[i])
		}
		result[key] = formatScalarValue(arr[i+1])
	}
	return result, nil
}

func formatScalarValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}
