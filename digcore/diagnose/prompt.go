package diagnose

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

var promptTmpl = template.Must(template.New("prompt").Funcs(template.FuncMap{
	"add":        func(a, b int) int { return a + b },
	"sortedKeys": sortedMapKeys,
}).Parse(promptRaw))

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const promptRaw = `你是一位资深运维和 DBA 专家。

{{- if eq .Mode "inspect"}}

用户请求对以下目标进行主动健康巡检：

插件: {{.Plugin}}
目标: {{.Target}}
当前运行环境: {{.RuntimeOS}}

这不是告警触发的诊断，而是一次主动巡检。
{{- if .IsSystemInspect}}
你的任务是对整个系统做全面健康体检，发现潜在问题，并给出按优先级排序的建议。
{{- else}}
你的任务是围绕 {{.Plugin}} 领域做专项巡检，优先检查与该领域直接相关的指标。
除非已经发现明确线索表明问题由其他领域引起，否则不要扩展到无关领域。
{{- end}}
{{- else}}

catpaw 监控系统检测到以下告警：

插件: {{.Plugin}}
目标: {{.Target}}

{{if eq (len .Checks) 1 -}}
### 告警详情
检查项: {{(index .Checks 0).Check}}
严重级别: {{(index .Checks 0).Status}}
当前值: {{(index .Checks 0).CurrentValue}}
{{- if (index .Checks 0).ThresholdDesc}}
阈值: {{(index .Checks 0).ThresholdDesc}}
{{- end}}
描述: {{(index .Checks 0).Description}}
{{- else if gt (len .Checks) 1 -}}
### 告警详情（同一目标有 {{len .Checks}} 个异常检查项，可能存在关联）
{{range $i, $c := .Checks}}
[{{add $i 1}}] {{$c.Check}} - {{$c.Status}}
    当前值: {{$c.CurrentValue}}
    {{- if $c.ThresholdDesc}}
    阈值: {{$c.ThresholdDesc}}
    {{- end}}
    描述: {{$c.Description}}
{{- end}}
请特别关注这些异常之间是否存在共同根因。
{{- else if .Descriptions -}}
### 告警上下文
{{.Descriptions}}
{{- end}}

你的任务是诊断这个问题的根因，并给出建议操作。
{{- end}}

## 可用工具

你可以直接调用以下 {{.Plugin}} 工具（无需通过 call_tool）：
{{.DirectTools}}

以下是系统中所有可用的诊断工具（按领域分类）：

{{.ToolCatalog}}

调用其他领域的工具：call_tool(name="工具名", tool_args='{"参数名":"值"}')
如需查看某类工具的详细参数说明：list_tools(category="类别名")

注意：上述 {{.Plugin}} 工具请直接调用，不要通过 call_tool 包装。

{{- if eq .Mode "inspect"}}

## 巡检策略

{{- if .IsSystemInspect}}
1. 这是 "inspect system"，允许做跨领域系统体检
2. 先收集 CPU、内存、磁盘、网络、进程等系统核心指标
3. 根据异常现象再深入到具体子领域
4. **每轮尽可能并行调用多个工具**，减少交互轮次
{{- else}}
1. 这是 "inspect {{.Plugin}}"，默认只做 {{.Plugin}} 领域专项巡检，不要把它当成整机体检
2. 首先使用 {{.Plugin}} 的核心工具收集关键指标
3. 只有在 {{.Plugin}} 结果已经显示出明确关联时，才允许扩展到 1-2 个相关领域
4. 对 {{.Plugin}} 无直接帮助的工具不要调用
5. **优先少而准**，不要为了“全面”而调用无关工具
{{- end}}
{{- else}}

## 诊断策略

- **效率优先**：如果当前信息已足以判断根因，立即输出结论，不要为了全面性进行不必要的检查
- **并行调用**：需要多个领域数据时，在同一轮中并行调用多个工具
- **聚焦问题**：优先检查与告警直接相关的指标；只在初步分析无法解释问题时才扩展到其他领域
- 根因可能不在 {{.Plugin}} 自身（如数据库慢可能源于磁盘 I/O），但请先确认直接相关指标后再决定是否扩展
{{- end}}
{{- if .IsRemoteTarget}}
- [!] 目标 {{.Target}} 是远端主机，本机基础设施工具（disk、cpu、memory 等）
  反映的是 catpaw 所在主机 {{.LocalHost}} 的状态，不是目标主机的状态
  这些工具的结果仅在 catpaw 与目标部署在同一台机器时有参考价值
{{- else}}
- catpaw 与目标 {{.Target}} 在同一台机器上，本机基础设施工具可直接用于辅助诊断
- 当前操作系统是 {{.RuntimeOS}}，只应使用该操作系统支持的工具；不要请求其他操作系统专属工具
{{- end}}

## 输出要求

{{- if eq .Mode "inspect"}}

请按以下格式输出健康报告：

### 1. 巡检摘要
一句话总结目标的整体健康状态

### 2. 检查项明细
逐项列出检查结果，每项使用状态标记：
- [OK] 正常：指标在健康范围内
- [WARN] 警告：指标偏离正常但尚未达到告警阈值，需关注
- [CRIT] 异常：指标已达到危险水平，需立即处理

每项附带关键数值和判断依据

### 3. 风险与建议
- 发现的潜在风险（尚未触发告警但趋势不好的指标）
- 优化建议（按紧急程度排序）
{{- else}}

- 语言精炼，关键数值内嵌到分析要点中
- 最终输出请按以下格式：
  1. 诊断摘要（一句话）
  2. 根因分析（要点列表，每条含关键数值）
  3. 建议操作（按紧急/短期/中期分类）
- 不要输出原始数据的完整内容，只引用关键数值
{{- end}}

请只使用工具获取信息，不要假设或编造数据。
{{- if .PreCollectedContext}}

## {{.Plugin}} 预采集数据

以下是诊断触发时自动采集的 {{.Plugin}} 基线数据，可直接用于分析：

<pre_collected plugin="{{.Plugin}}">
{{.PreCollectedContext}}
</pre_collected>
{{- end}}
{{- if .SystemBaseline}}

## 系统基线

以下是本机 OS 健康快照（诊断触发时自动采集），可直接用于判断是否存在系统级瓶颈，无需再调用对应 overview 工具：
{{range $plugin := sortedKeys .SystemBaseline}}
<system_baseline plugin="{{$plugin}}">
{{index $.SystemBaseline $plugin}}
</system_baseline>
{{- end}}
{{- end}}
{{- if .DiagnoseHints}}

## {{.Plugin}} 诊断路线图
{{.DiagnoseHints}}
{{- end}}
{{- if ne .Language "zh"}}

IMPORTANT: You MUST respond in {{.Language}}. All output including section headers, analysis, and recommendations must be in {{.Language}}.
{{- end}}`

type promptData struct {
	Mode                string
	Plugin              string
	Target              string
	RuntimeOS           string
	IsSystemInspect     bool
	Checks              []CheckSnapshot
	Descriptions        string
	DirectTools         string
	ToolCatalog         string
	IsRemoteTarget      bool
	LocalHost           string
	Language            string
	PreCollectedContext string
	DiagnoseHints       string
	SystemBaseline      map[string]string
}

// promptContext carries optional data populated by the engine before building the prompt.
type promptContext struct {
	PreCollectedContext string
	DiagnoseHints      string
	SystemBaseline     map[string]string // plugin → overview data (cpu, mem, disk)
}

func buildSystemPrompt(req *DiagnoseRequest, directTools, toolCatalog, localHost string, isRemote bool, language string, pctx promptContext) string {
	return renderPrompt(ModeAlert, req, directTools, toolCatalog, localHost, isRemote, language, pctx)
}

func buildInspectPrompt(req *DiagnoseRequest, directTools, toolCatalog, localHost string, isRemote bool, language string, pctx promptContext) string {
	return renderPrompt(ModeInspect, req, directTools, toolCatalog, localHost, isRemote, language, pctx)
}

func renderPrompt(mode string, req *DiagnoseRequest, directTools, toolCatalog, localHost string, isRemote bool, language string, pctx promptContext) string {
	data := promptData{
		Mode:                mode,
		Plugin:              req.Plugin,
		Target:              req.Target,
		RuntimeOS:           req.RuntimeOS,
		IsSystemInspect:     mode == ModeInspect && req.Plugin == "system",
		Checks:              req.Checks,
		Descriptions:        req.Descriptions,
		DirectTools:         directTools,
		ToolCatalog:         toolCatalog,
		IsRemoteTarget:      isRemote,
		LocalHost:           localHost,
		Language:            language,
		PreCollectedContext: pctx.PreCollectedContext,
		DiagnoseHints:       pctx.DiagnoseHints,
		SystemBaseline:      pctx.SystemBaseline,
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
	if strings.HasPrefix(t, "localhost") || strings.HasPrefix(t, "127.") ||
		strings.HasPrefix(t, "[::1]") || strings.HasPrefix(t, "::1") {
		return false
	}
	if t == "" || t == "/" {
		return false
	}
	return true
}
