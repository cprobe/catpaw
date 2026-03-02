package diagnose

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

var systemPromptTmpl = template.Must(template.New("system").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
}).Parse(systemPromptRaw))

const systemPromptRaw = `你是一位资深运维和 DBA 专家。catpaw 监控系统检测到以下告警：

插件: {{.Plugin}}
目标: {{.Target}}

{{if eq (len .Checks) 1 -}}
### 告警详情
检查项: {{(index .Checks 0).Check}}
严重级别: {{(index .Checks 0).Status}}
当前值: {{(index .Checks 0).CurrentValue}}
阈值: warning={{(index .Checks 0).WarningThreshold}}, critical={{(index .Checks 0).CriticalThreshold}}
描述: {{(index .Checks 0).Description}}
{{- else -}}
### 告警详情（同一目标有 {{len .Checks}} 个异常检查项，可能存在关联）
{{range $i, $c := .Checks}}
[{{add $i 1}}] {{$c.Check}} - {{$c.Status}}
    当前值: {{$c.CurrentValue}}
    阈值: warning={{$c.WarningThreshold}}, critical={{$c.CriticalThreshold}}
    描述: {{$c.Description}}
{{- end}}
请特别关注这些异常之间是否存在共同根因。
{{- end}}

你的任务是诊断这个问题的根因，并给出建议操作。

## 可用工具

你可以直接调用以下 {{.Plugin}} 工具（无需通过 call_tool）：
{{.DirectTools}}

如需使用其他领域的工具（磁盘、CPU、内存、网络等），请：
1. 调用 list_tool_categories() 查看可用工具大类
2. 调用 list_tools(category) 查看某个大类下的具体工具
3. 调用 call_tool(name, tool_args) 执行具体工具
   tool_args 为 JSON 字符串格式，如 call_tool(name="disk_iostat", tool_args='{"device":"sda"}')

注意：上述 {{.Plugin}} 工具请直接调用，不要通过 call_tool 包装。

## 诊断提示

- 根因可能不在 {{.Plugin}} 自身，例如数据库慢可能是磁盘 I/O 瓶颈，
  服务延迟可能是 CPU 或内存压力，请根据需要探索其他领域的工具
{{- if .IsRemoteTarget}}
- ⚠️ 目标 {{.Target}} 是远端主机，本机基础设施工具（disk、cpu、memory 等）
  反映的是 catpaw 所在主机 {{.LocalHost}} 的状态，不是目标主机的状态
  这些工具的结果仅在 catpaw 与目标部署在同一台机器时有参考价值
{{- else}}
- catpaw 与目标 {{.Target}} 在同一台机器上，本机基础设施工具可直接用于辅助诊断
{{- end}}

## 输出要求

- 请只使用工具获取信息，不要假设或编造数据
- 语言精炼，关键数值内嵌到分析要点中
- 最终输出请按以下格式：
  1. 诊断摘要（一句话）
  2. 根因分析（要点列表，每条含关键数值）
  3. 建议操作（按紧急/短期/中期分类）
- 不要输出原始数据的完整内容，只引用关键数值`

type promptData struct {
	Plugin         string
	Target         string
	Checks         []CheckSnapshot
	DirectTools    string
	IsRemoteTarget bool
	LocalHost      string
}

func buildSystemPrompt(req *DiagnoseRequest, directTools string, localHost string, isRemote bool) string {
	data := promptData{
		Plugin:         req.Plugin,
		Target:         req.Target,
		Checks:         req.Checks,
		DirectTools:    directTools,
		IsRemoteTarget: isRemote,
		LocalHost:      localHost,
	}

	var buf bytes.Buffer
	if err := systemPromptTmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("Error building prompt: %v", err)
	}
	return buf.String()
}

// formatDirectTools generates a list of directly-injected plugin tools for the prompt.
func formatDirectTools(tools []DiagnoseTool) string {
	if len(tools) == 0 {
		return "(无直接工具)"
	}
	var b strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
		for _, p := range t.Parameters {
			req := ""
			if p.Required {
				req = " (必需)"
			}
			fmt.Fprintf(&b, "  参数 %s (%s): %s%s\n", p.Name, p.Type, p.Description, req)
		}
	}
	return b.String()
}

// isRemoteTarget determines if the target is a remote host (not localhost).
func isRemoteTarget(target string) bool {
	t := strings.ToLower(target)
	if strings.HasPrefix(t, "localhost") || strings.HasPrefix(t, "127.") || strings.HasPrefix(t, "[::1]") {
		return false
	}
	if t == "" || t == "/" {
		return false
	}
	return true
}
