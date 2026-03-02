package diagnose

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

var promptTmpl = template.Must(template.New("prompt").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
}).Parse(promptRaw))

const promptRaw = `你是一位资深运维和 DBA 专家。

{{- if eq .Mode "inspect"}}

用户请求对以下目标进行主动健康巡检：

插件: {{.Plugin}}
目标: {{.Target}}

这不是告警触发的诊断，而是一次主动巡检。你的任务是全面检查目标的健康状态，发现潜在问题。
{{- else}}

catpaw 监控系统检测到以下告警：

插件: {{.Plugin}}
目标: {{.Target}}

{{if eq (len .Checks) 1 -}}
### 告警详情
检查项: {{(index .Checks 0).Check}}
严重级别: {{(index .Checks 0).Status}}
当前值: {{(index .Checks 0).CurrentValue}}
阈值: warning={{(index .Checks 0).WarningThreshold}}, critical={{(index .Checks 0).CriticalThreshold}}
描述: {{(index .Checks 0).Description}}
{{- else if gt (len .Checks) 1 -}}
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
{{- end}}

## 可用工具

你可以直接调用以下 {{.Plugin}} 工具（无需通过 call_tool）：
{{.DirectTools}}

如需使用其他领域的工具（磁盘、CPU、内存、网络等），请：
1. 调用 list_tool_categories() 查看可用工具大类
2. 调用 list_tools(category) 查看某个大类下的具体工具
3. 调用 call_tool(name, tool_args) 执行具体工具
   tool_args 为 JSON 字符串格式，如 call_tool(name="disk_usage", tool_args='{}')

注意：上述 {{.Plugin}} 工具请直接调用，不要通过 call_tool 包装。

{{- if eq .Mode "inspect"}}

## 巡检策略

1. 首先使用 {{.Plugin}} 的核心工具收集关键指标
2. 根据初步结果，针对性地深入检查可疑领域
3. 如果是远端服务，同时关注基础设施层面可能影响服务的因素
{{- else}}

## 诊断提示

- 根因可能不在 {{.Plugin}} 自身，例如数据库慢可能是磁盘 I/O 瓶颈，
  服务延迟可能是 CPU 或内存压力，请根据需要探索其他领域的工具
{{- end}}
{{- if .IsRemoteTarget}}
- ⚠️ 目标 {{.Target}} 是远端主机，本机基础设施工具（disk、cpu、memory 等）
  反映的是 catpaw 所在主机 {{.LocalHost}} 的状态，不是目标主机的状态
  这些工具的结果仅在 catpaw 与目标部署在同一台机器时有参考价值
{{- else}}
- catpaw 与目标 {{.Target}} 在同一台机器上，本机基础设施工具可直接用于辅助诊断
{{- end}}

## 输出要求

{{- if eq .Mode "inspect"}}

请按以下格式输出健康报告：

### 1. 巡检摘要
一句话总结目标的整体健康状态

### 2. 检查项明细
逐项列出检查结果，每项使用状态标记：
- 🟢 正常：指标在健康范围内
- 🟡 警告：指标偏离正常但尚未达到告警阈值，需关注
- 🔴 异常：指标已达到危险水平，需立即处理

每项附带关键数值和判断依据

### 3. 风险与建议
- 发现的潜在风险（尚未触发告警但趋势不好的指标）
- 优化建议（按紧急程度排序）
{{- else}}

- 请只使用工具获取信息，不要假设或编造数据
- 语言精炼，关键数值内嵌到分析要点中
- 最终输出请按以下格式：
  1. 诊断摘要（一句话）
  2. 根因分析（要点列表，每条含关键数值）
  3. 建议操作（按紧急/短期/中期分类）
- 不要输出原始数据的完整内容，只引用关键数值
{{- end}}

请只使用工具获取信息，不要假设或编造数据。`

type promptData struct {
	Mode           string
	Plugin         string
	Target         string
	Checks         []CheckSnapshot
	DirectTools    string
	IsRemoteTarget bool
	LocalHost      string
}

func buildSystemPrompt(req *DiagnoseRequest, directTools string, localHost string, isRemote bool) string {
	return renderPrompt(ModeAlert, req, directTools, localHost, isRemote)
}

func buildInspectPrompt(req *DiagnoseRequest, directTools string, localHost string, isRemote bool) string {
	return renderPrompt(ModeInspect, req, directTools, localHost, isRemote)
}

func renderPrompt(mode string, req *DiagnoseRequest, directTools string, localHost string, isRemote bool) string {
	data := promptData{
		Mode:           mode,
		Plugin:         req.Plugin,
		Target:         req.Target,
		Checks:         req.Checks,
		DirectTools:    directTools,
		IsRemoteTarget: isRemote,
		LocalHost:      localHost,
	}

	var buf bytes.Buffer
	if err := promptTmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("Error building prompt: %v", err)
	}
	return buf.String()
}

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
