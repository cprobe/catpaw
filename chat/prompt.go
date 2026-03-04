package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/logger"
)

var chatPromptTmpl = template.Must(template.New("chat").Parse(chatPromptRaw))

const chatPromptRaw = `你是 catpaw 运维助手，运行在主机 {{.Hostname}}（{{.IP}}）上。
操作系统: {{.OS}} {{.Arch}} {{.Kernel}}

用户通过 SSH 登录了这台机器，正在与你对话排查问题。
{{- if .SystemSnapshot}}

## 系统快照（启动时自动采集）

{{.SystemSnapshot}}
{{- end}}
{{- if .MCPIdentity}}

## 外部数据源（MCP）

已连接的 MCP 服务器及本机在各数据源中的过滤条件：
{{.MCPIdentity}}
**查询规则**：
1. 过滤条件用于定位本机数据，查询时必须使用它来筛选
2. 不确定数据名称或字段时，优先使用该数据源的发现/列举类工具探测，不要猜测
{{- end}}

## 可用诊断工具

以下是所有已注册的诊断工具，你可以直接调用：

{{.ToolCatalog}}

调用方式：call_tool(name="工具名", tool_args='{"参数名":"值"}')
示例：call_tool(name="disk_usage", tool_args='{}')

你还可以调用 exec_shell(command="命令") 执行任意 shell 命令（需用户确认后才会执行）。

## 工作原则

- 回答问题时优先使用工具获取实时数据，不要猜测或编造
- **需要多个领域数据时，在同一轮中并行调用多个工具**，不要逐个调用
  例如排查"机器为什么慢"时，一次性调用 cpu_usage、memory_usage、disk_usage 等
- 回答简洁，关键数值内嵌到分析中
- 如果用户的问题不需要工具就能回答（尤其是系统快照中已有的信息），直接回答
- 如果用户的问题超出你的工具能力范围，坦诚告知
- exec_shell 会经过用户确认，放心提出你需要执行的命令
- 默认使用 {{.Language}} 回复，但如果用户要求切换语言，按用户要求输出`

type chatPromptData struct {
	Hostname       string
	IP             string
	OS             string
	Arch           string
	Kernel         string
	ToolCatalog    string
	SystemSnapshot string
	MCPIdentity    string
	Language       string
}

func buildChatSystemPrompt(registry *diagnose.ToolRegistry, snapshot, mcpIdentity, language string) string {
	if language == "" {
		language = "中文"
	}
	hostname, _ := os.Hostname()
	ip := getLocalIP()
	kernel := getKernelVersion()

	data := chatPromptData{
		Hostname:       hostname,
		IP:             ip,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Kernel:         kernel,
		ToolCatalog:    registry.ListToolCatalogSmart(),
		SystemSnapshot: snapshot,
		MCPIdentity:    mcpIdentity,
		Language:       language,
	}

	logger.Logger.Debugf("[chat] ToolCatalog len=%d content:\n%s", len(data.ToolCatalog), data.ToolCatalog)

	var buf bytes.Buffer
	if err := chatPromptTmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("Error building prompt: %v", err)
	}
	return buf.String()
}

func getLocalIP() string {
	out, err := execCommand("hostname", "-I")
	if err != nil {
		out, err = execCommand("hostname", "-i")
	}
	if err == nil {
		if parts := strings.Fields(strings.TrimSpace(out)); len(parts) > 0 {
			return parts[0]
		}
	}
	// Fallback: use Go's net package (works on macOS and other platforms)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return "unknown"
}

func getKernelVersion() string {
	out, err := execCommand("uname", "-r")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

func execCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
