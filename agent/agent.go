package agent

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/cprobe/catpaw/chat"
	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/diagnose/aiclient"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/notify"
	"github.com/cprobe/catpaw/digcore/pkg/choice"
	"github.com/cprobe/catpaw/digcore/plugins"
	"github.com/cprobe/catpaw/digcore/server"
	"github.com/toolkits/pkg/file"

	// auto registry
	_ "github.com/cprobe/catpaw/plugins/cert"
	_ "github.com/cprobe/catpaw/plugins/conntrack"
	_ "github.com/cprobe/catpaw/plugins/cpu"
	_ "github.com/cprobe/catpaw/plugins/disk"
	_ "github.com/cprobe/catpaw/plugins/diskio"
	_ "github.com/cprobe/catpaw/plugins/dns"
	_ "github.com/cprobe/catpaw/plugins/docker"
	_ "github.com/cprobe/catpaw/plugins/exec"
	_ "github.com/cprobe/catpaw/plugins/filecheck"
	_ "github.com/cprobe/catpaw/plugins/filefd"
	_ "github.com/cprobe/catpaw/plugins/hostident"
	_ "github.com/cprobe/catpaw/plugins/http"
	_ "github.com/cprobe/catpaw/plugins/journaltail"
	_ "github.com/cprobe/catpaw/plugins/logfile"
	_ "github.com/cprobe/catpaw/plugins/mem"
	_ "github.com/cprobe/catpaw/plugins/mount"
	_ "github.com/cprobe/catpaw/plugins/neigh"
	_ "github.com/cprobe/catpaw/plugins/net"
	_ "github.com/cprobe/catpaw/plugins/netif"
	_ "github.com/cprobe/catpaw/plugins/ntp"
	_ "github.com/cprobe/catpaw/plugins/ping"
	_ "github.com/cprobe/catpaw/plugins/procfd"
	_ "github.com/cprobe/catpaw/plugins/procnum"
	_ "github.com/cprobe/catpaw/plugins/redis"
	_ "github.com/cprobe/catpaw/plugins/redis_sentinel"
	_ "github.com/cprobe/catpaw/plugins/scriptfilter"
	_ "github.com/cprobe/catpaw/plugins/secmod"
	_ "github.com/cprobe/catpaw/plugins/sockstat"
	_ "github.com/cprobe/catpaw/plugins/sysctl"
	_ "github.com/cprobe/catpaw/plugins/sysdiag"
	_ "github.com/cprobe/catpaw/plugins/systemd"
	_ "github.com/cprobe/catpaw/plugins/tcpstate"
	_ "github.com/cprobe/catpaw/plugins/uptime"
	_ "github.com/cprobe/catpaw/plugins/zombie"
)

// diagnoseRunnerAdapter bridges diagnose.DiagnoseEngine to server.DiagnoseRunner.
type diagnoseRunnerAdapter struct {
	engine *diagnose.DiagnoseEngine
}

func (a *diagnoseRunnerAdapter) RunStreaming(ctx context.Context, mode, plugin, target string, params map[string]any, cb server.StreamCallback) (string, error) {
	req := &diagnose.DiagnoseRequest{
		Mode:      mode,
		Plugin:    plugin,
		Target:    target,
		RuntimeOS: runtime.GOOS,
	}
	if desc, _ := params["descriptions"].(string); desc != "" {
		req.Descriptions = desc
	}
	return a.engine.RunDiagnoseStreaming(ctx, req, diagnose.StreamCallback(cb))
}

// chatRunnerAdapter bridges diagnose.ChatStream to server.ChatRunner.
type chatRunnerAdapter struct{}

func (a *chatRunnerAdapter) NewSession(ctx context.Context, opts server.ChatSessionOpts, cb server.StreamCallback) (server.ChatHandle, error) {
	cfg := config.Config.AI
	if !cfg.Enabled {
		return nil, fmt.Errorf("AI is not enabled")
	}

	eng := diagnose.GlobalEngine()
	if eng == nil {
		return nil, fmt.Errorf("diagnose engine not initialized")
	}

	registry := eng.Registry()
	fc := aiclient.NewFailoverClientForScene(cfg, "chat")

	snapshotStart := time.Now()
	snapshot := chat.CollectSnapshot(registry)
	logger.Logger.Infow("chat_snapshot_completed",
		"duration_ms", time.Since(snapshotStart).Milliseconds())

	systemPrompt := chat.BuildChatSystemPrompt(registry, snapshot, cfg.Language, opts.AllowShell)

	var shellExec diagnose.ShellExecutor
	if opts.AllowShell {
		shellExec = &remoteShellExecutor{cb: cb}
	}

	progressCb := newRemoteProgressCallback(cb)

	sess := diagnose.NewChatStream(diagnose.ChatStreamConfig{
		FC:                 fc,
		Registry:           registry,
		ToolTimeout:        time.Duration(cfg.ToolTimeout),
		SystemPrompt:       systemPrompt,
		AllowShell:         opts.AllowShell,
		ShellExecutor:      shellExec,
		ProgressCallback:   progressCb,
		ContextWindowLimit: cfg.ContextWindowLimit(),
		GatewayMetadata:    aiclient.GatewayMetadata{RequestSource: "remote_chat"},
	})

	return &chatStreamHandle{sess: sess}, nil
}

// chatStreamHandle adapts diagnose.ChatStream to server.ChatHandle.
type chatStreamHandle struct {
	sess *diagnose.ChatStream
}

func (h *chatStreamHandle) HandleMessage(ctx context.Context, input string) (string, error) {
	reply, _, err := h.sess.HandleMessage(ctx, input)
	return reply, err
}

// remoteShellExecutor implements diagnose.ShellExecutor for remote sessions.
type remoteShellExecutor struct {
	cb server.StreamCallback
}

func (r *remoteShellExecutor) ExecuteShell(ctx context.Context, command string, timeout time.Duration) (string, bool, error) {
	r.cb(fmt.Sprintf("[Shell] %s", command), "tool_call", false, nil)
	output, err := chat.ExecShell(ctx, command, timeout)
	return output, true, err
}

// newRemoteProgressCallback creates a ProgressCallback that streams events via WebSocket.
func newRemoteProgressCallback(cb server.StreamCallback) diagnose.ProgressCallback {
	return func(event diagnose.ProgressEvent) {
		switch event.Type {
		case diagnose.ProgressAIStart:
			cb(fmt.Sprintf("[Round %d] thinking...", event.Round), "thinking", false, nil)
		case diagnose.ProgressAIDone:
			if event.Duration > 0 {
				cb(fmt.Sprintf("[Round %d done] %.1fs", event.Round, event.Duration.Seconds()), "thinking", false, nil)
			}
			if event.Reasoning != "" {
				cb(event.Reasoning, "answer", false, nil)
			}
		case diagnose.ProgressToolStart:
			cb(fmt.Sprintf("[Tool] %s %s", event.ToolName, event.ToolArgs), "tool_call", false, nil)
		case diagnose.ProgressToolDone:
			status := "ok"
			if event.IsError {
				status = "error"
			}
			cb(fmt.Sprintf("[Tool done] %s (%s, %dB, %s)", event.ToolName, status, event.ResultLen, event.Duration), "tool_result", false, nil)
		}
	}
}

type PluginConfig struct {
	Source      string // file || http
	Digest      string
	FileContent []byte
}

type Agent struct {
	pluginFilters map[string]struct{}
	pluginConfigs map[string]*PluginConfig
	pluginRunners map[string]*PluginRunner
	cancel        context.CancelFunc
	startTime     time.Time
	Version       string
	sync.RWMutex
}

func New(version string) *Agent {
	return &Agent{
		pluginFilters: parseFilter(config.Config.Plugins),
		pluginConfigs: make(map[string]*PluginConfig),
		pluginRunners: make(map[string]*PluginRunner),
		startTime:     time.Now(),
		Version:       version,
	}
}

// Run starts the agent and blocks until a termination signal is received.
// This is the public entry point for external binaries (e.g. kubepaw) that
// embed catpaw's full agent lifecycle.
func Run(version string) {
	ag := New(version)
	ag.Start()
	waitForSignal(ag)
	ag.Stop()
	logger.Logger.Info("agent exited")
}

func (a *Agent) Start() {
	logger.Logger.Info("agent starting")

	initNotifiers()
	a.initDiagnoseEngine()

	pcs, err := loadFileConfigs()
	if err != nil {
		logger.Logger.Errorw("load file configs fail", "error", err)
		return
	}

	for name, pc := range pcs {
		a.LoadPlugin(name, pc)
	}

	a.startServerConn()

	logger.Logger.Info("agent started")
}

func (a *Agent) startServerConn() {
	if !config.Config.Server.Enabled {
		return
	}

	server.InitAlertBuffer(config.Config.Server.GetAlertBufferSize())

	if eng := diagnose.GlobalEngine(); eng != nil {
		server.SetConcurrencyLimiter(eng)
		server.SetDiagnoseRunner(&diagnoseRunnerAdapter{engine: eng})
		server.SetChatRunner(&chatRunnerAdapter{})
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	pluginNames := make([]string, 0, len(a.pluginRunners))
	for name := range a.pluginRunners {
		pluginNames = append(pluginNames, name)
	}

	go server.RunForever(ctx, a.startTime, pluginNames, a.Version)
}

func initNotifiers() {
	if cfg := config.Config.Notify.Console; cfg != nil && cfg.Enabled {
		notify.Register(notify.NewConsoleNotifier())
	}
	if cfg := config.Config.Notify.Flashduty; cfg != nil && cfg.IntegrationKey != "" {
		notify.Register(notify.NewFlashdutyNotifier(cfg))
	}
	if cfg := config.Config.Notify.PagerDuty; cfg != nil && cfg.RoutingKey != "" {
		notify.Register(notify.NewPagerDutyNotifier(cfg))
	}
	if cfg := config.Config.Notify.WebAPI; cfg != nil && cfg.URL != "" {
		notify.Register(notify.NewWebAPINotifier(cfg))
	}
	if config.Config.Server.Enabled {
		notify.Register(server.NewServerNotifier())
	}
}

func (a *Agent) initDiagnoseEngine() {
	if !config.Config.AI.Enabled {
		logger.Logger.Infow("AI diagnose disabled")
		return
	}
	if err := config.Config.AI.Validate(); err != nil {
		logger.Logger.Errorw("AI diagnose initialization skipped", "error", err)
		return
	}
	registry := diagnose.NewToolRegistry()
	for _, creator := range plugins.PluginCreators {
		plugins.MayRegisterDiagnoseTools(creator(), registry)
	}
	for _, r := range plugins.DiagnoseRegistrars {
		r(registry)
	}
	diagnose.Init(registry)
}

func (a *Agent) LoadPlugin(name string, pc *PluginConfig) {
	if len(a.pluginFilters) > 0 {
		// need filter by --plugins
		_, has := a.pluginFilters[name]
		if !has {
			return
		}
	}

	logger.Logger.Infow("loading plugin", "plugin", name)

	creator, has := plugins.PluginCreators[name]
	if !has {
		logger.Logger.Infow("plugin not supported", "plugin", name)
		return
	}

	pluginObject := creator()
	err := toml.Unmarshal(pc.FileContent, pluginObject)
	if err != nil {
		logger.Logger.Errorw("unmarshal plugin config fail", "plugin", name, "error", err)
		return
	}

	// structs will have value after toml.Unmarshal
	// apply partial configuration if some fields are not set
	err = plugins.MayApplyPartials(pluginObject)
	if err != nil {
		logger.Logger.Errorw("apply partial config fail", "plugin", name, "error", err)
		return
	}

	runner := newPluginRunner(name, pluginObject)
	runner.start()

	a.Lock()
	a.pluginRunners[name] = runner
	a.pluginConfigs[name] = pc
	a.Unlock()
}

func (a *Agent) DelPlugin(name string) {
	a.Lock()
	defer a.Unlock()

	if runner, has := a.pluginRunners[name]; has {
		runner.stop()
		delete(a.pluginRunners, name)
		delete(a.pluginConfigs, name)
	}
}

func (a *Agent) RunningPlugins() []string {
	a.RLock()
	defer a.RUnlock()

	cnt := len(a.pluginRunners)
	ret := make([]string, 0, cnt)

	for name := range a.pluginRunners {
		ret = append(ret, name)
	}

	return ret
}

func (a *Agent) GetPluginConfig(name string) *PluginConfig {
	a.RLock()
	defer a.RUnlock()

	return a.pluginConfigs[name]
}

func (a *Agent) Stop() {
	logger.Logger.Info("agent stopping")

	if a.cancel != nil {
		a.cancel()
	}

	diagnose.Shutdown()

	a.Lock()
	defer a.Unlock()

	for name := range a.pluginRunners {
		a.pluginRunners[name].stop()
		delete(a.pluginRunners, name)
		delete(a.pluginConfigs, name)
	}

	logger.Logger.Info("agent stopped")
}

func (a *Agent) HandleChangedPlugin(names []string) {
	for _, name := range names {
		pc := a.GetPluginConfig(name)
		if pc.Source != "file" {
			continue
		}

		mtime, content, err := readPluginDir(name)
		if err != nil {
			logger.Logger.Errorw("read plugin dir fail", "plugin", name, "error", err)
			continue
		}

		if mtime == -1 || len(content) == 0 {
			a.DelPlugin(name)
			continue
		}

		if pc.Digest == fmt.Sprint(mtime) {
			continue
		}

		a.DelPlugin(name)
		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: content,
		})
	}
}

func (a *Agent) Reload() {
	logger.Logger.Info("agent reloading")

	names := a.RunningPlugins()
	a.HandleChangedPlugin(names)
	a.HandleNewPlugin(names)

	logger.Logger.Info("agent reloaded")
}

func (a *Agent) HandleNewPlugin(names []string) {
	dirs, err := file.DirsUnder(config.Config.ConfigDir)
	if err != nil {
		logger.Logger.Errorw("failed to get config dirs", "error", err)
		return
	}

	for _, dir := range dirs {
		if !strings.HasPrefix(dir, "p.") {
			continue
		}

		name := dir[len("p."):]

		if choice.Contains(name, names) {
			continue
		}

		mtime, content, err := readPluginDir(name)
		if err != nil {
			logger.Logger.Errorw("read plugin dir fail", "plugin", name, "error", err)
			continue
		}

		if mtime == -1 || len(content) == 0 {
			continue
		}

		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: content,
		})
	}
}
