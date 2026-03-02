package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/cprobe/catpaw/diagnose"
)

var chatPromptTmpl = template.Must(template.New("chat").Parse(chatPromptRaw))

const chatPromptRaw = `你是 catpaw 运维助手，运行在主机 {{.Hostname}}（{{.IP}}）上。
操作系统: {{.OS}} {{.Arch}} {{.Kernel}}

用户通过 SSH 登录了这台机器，正在与你对话排查问题。

## 你的能力

你可以调用诊断工具获取这台机器的实时数据：

{{.ToolCategories}}

使用方式：
1. 调用 list_tools(category) 查看某个大类下的具体工具
2. 调用 call_tool(name, tool_args) 执行具体工具
   tool_args 为 JSON 字符串格式，如 call_tool(name="disk_usage", tool_args='{}')

你还可以调用 exec_shell(command) 执行任意 shell 命令（需用户确认后才会执行）。
当内置工具无法满足需求时，优先使用 exec_shell。

## 工作原则

- 回答问题时优先使用工具获取实时数据，不要猜测或编造
- 回答简洁，关键数值内嵌到分析中
- 如果用户的问题不需要工具就能回答，直接回答即可
- 如果用户的问题超出你的工具能力范围，坦诚告知
- exec_shell 会经过用户确认，放心提出你需要执行的命令
- 默认使用 {{.Language}} 回复，但如果用户要求切换语言，按用户要求输出`

type chatPromptData struct {
	Hostname       string
	IP             string
	OS             string
	Arch           string
	Kernel         string
	ToolCategories string
	Language       string
}

func buildChatSystemPrompt(registry *diagnose.ToolRegistry, language string) string {
	hostname, _ := os.Hostname()
	ip := getLocalIP()
	kernel := getKernelVersion()

	data := chatPromptData{
		Hostname:       hostname,
		IP:             ip,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Kernel:         kernel,
		ToolCategories: registry.ListCategories(),
		Language:       language,
	}

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
		if err != nil {
			return "unknown"
		}
	}
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) > 0 {
		return parts[0]
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
