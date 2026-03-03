package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chai2010/winsvc"
	"github.com/cprobe/catpaw/agent"
	"github.com/cprobe/catpaw/chat"
	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/winx"
	"github.com/toolkits/pkg/runner"
)

var (
	appPath     string
	configDir   = flag.String("configs", "conf.d", "Configuration directory")
	showVersion = flag.Bool("version", false, "Show version")
	loglevel    = flag.String("loglevel", "", "Log level (debug/info/warn/error)")
)

func init() {
	var err error
	if appPath, err = winsvc.GetAppPath(); err != nil {
		panic(err)
	}

	flag.Usage = printUsage
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Println(config.Version)
		os.Exit(0)
	}

	args := flag.Args()

	if len(args) > 0 && args[0] == "help" {
		if len(args) >= 2 {
			printSubcommandHelp(args[1])
		} else {
			printUsage()
		}
		return
	}

	if !handleSubcommand(args) {
		printUsage()
	}
}

func handleSubcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "run":
		handleRunSubcommand(args)
		return true
	case "diagnose":
		handleDiagnoseSubcommand(args)
		return true
	case "inspect":
		handleInspectSubcommand(args)
		return true
	case "chat":
		handleChatSubcommand()
		return true
	case "selftest":
		handleSelftestSubcommand(args)
		return true
	default:
		return false
	}
}

func handleRunSubcommand(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	testMode := fs.Bool("test", false, "Test mode: print results to stdout")
	interval := fs.Int64("interval", 0, "Global collection interval (seconds)")
	pluginFilter := fs.String("plugins", "", "Plugin filter (e.g. redis:cpu:disk)")
	fs.Usage = printRunUsage
	fs.Parse(args[1:])

	winx.Args(appPath)

	if err := config.InitConfig(*configDir, *testMode, *interval, *pluginFilter, *loglevel); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	closefn := logger.Build()
	defer closefn()

	runner.Init()
	logger.Logger.Infow("runner initialized",
		"binarydir", runner.Cwd,
		"configdir", *configDir,
		"hostname", runner.Hostname,
		"fd_limits", runner.FdLimits(),
	)

	ag := agent.New()

	if runtime.GOOS == "windows" && !winsvc.IsAnInteractiveSession() {
		if err := winsvc.RunAsService(winx.GetServiceName(), ag.Start, ag.Stop, false); err != nil {
			fmt.Println("failed to run windows service:", err)
			os.Exit(1)
		}
		return
	} else {
		ag.Start()
	}

	waitForSignal(ag)

	ag.Stop()
	logger.Logger.Info("agent exited")
}

func handleDiagnoseSubcommand(args []string) {
	stateDir := filepath.Join(filepath.Dir(*configDir), "state.d")

	if len(args) < 2 {
		printDiagnoseUsage()
		return
	}

	switch args[1] {
	case "list":
		if err := diagnose.CLIList(stateDir, 50); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "show":
		if len(args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: catpaw diagnose show <record-id>\n")
			os.Exit(1)
		}
		if err := diagnose.CLIShow(stateDir, args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		printDiagnoseUsage()
	}
}

func handleChatSubcommand() {
	if err := config.InitConfig(*configDir, false, 0, "", *loglevel); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if err := chat.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func handleInspectSubcommand(args []string) {
	if err := config.InitConfig(*configDir, false, 0, "", *loglevel); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	closefn := logger.Build()
	defer closefn()

	if len(args) < 2 {
		printInspectUsage()
		os.Exit(1)
	}

	pluginName := args[1]

	var target string
	if len(args) >= 3 {
		target = args[2]
	}

	if err := agent.RunInspect(pluginName, target); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func handleSelftestSubcommand(args []string) {
	registry := diagnose.NewToolRegistry()
	for _, creator := range plugins.PluginCreators {
		plugins.MayRegisterDiagnoseTools(creator(), registry)
	}
	for _, r := range plugins.DiagnoseRegistrars {
		r(registry)
	}

	filter := ""
	verbose := true
	for _, a := range args[1:] {
		if a == "-q" || a == "--quiet" {
			verbose = false
		} else if !strings.HasPrefix(a, "-") {
			filter = a
		}
	}

	diagnose.RunSelfTest(registry, filter, verbose)
}

// --- Usage ---

func printUsage() {
	fmt.Fprintf(os.Stderr, `catpaw %s - Lightweight monitoring agent with AI-powered diagnostics

Usage:
  catpaw run [flags]                      Start the monitoring agent
  catpaw chat                             Interactive AI chat for troubleshooting
  catpaw inspect <plugin> [target]        Run health inspection on a target
  catpaw diagnose <command>               Manage diagnosis records
  catpaw selftest [filter] [-q]           Smoke-test all diagnostic tools
  catpaw help [command]                   Show help for a command

Global Flags:
  --configs <dir>    Configuration directory (default: conf.d)
  --loglevel <lvl>   Log level: debug/info/warn/error
  --version          Show version

Commands:
  run         Start the monitoring agent (use 'catpaw help run' for flags)
  chat        Interactive AI chat for troubleshooting
  inspect     Proactive health inspection (AI-powered)
  diagnose    View past diagnosis / inspection records
  selftest    Smoke-test all diagnostic tools on this machine

Run 'catpaw help <command>' for details on a specific command.
`, config.Version)
}

func printSubcommandHelp(cmd string) {
	switch cmd {
	case "run":
		printRunUsage()
	case "chat":
		printChatUsage()
	case "inspect":
		printInspectUsage()
	case "diagnose":
		printDiagnoseUsage()
	case "selftest":
		printSelftestUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %q\n\n", cmd)
		printUsage()
	}
}

func printRunUsage() {
	fmt.Println(`Usage: catpaw run [flags]

Start the monitoring agent. Collects metrics, evaluates alerts,
and optionally triggers AI-powered diagnosis.

Flags:
  --test              Test mode: print results to stdout instead of forwarding
  --interval <sec>    Override global collection interval (seconds)
  --plugins <list>    Plugin filter, colon-separated (e.g. redis:cpu:disk)

Examples:
  catpaw run                              Start with default config
  catpaw run --test                       Print metrics to stdout
  catpaw run --plugins redis:cpu          Only run redis and cpu plugins
  catpaw --configs /etc/catpaw/conf.d run Start with custom config dir`)
}

func printChatUsage() {
	fmt.Println(`Usage: catpaw chat

Start an interactive AI-powered chat session for troubleshooting.
The AI can use built-in diagnostic tools and execute shell commands
(with user confirmation) to help investigate issues on this machine.

Requires [ai] enabled = true in config.toml.

Examples:
  catpaw chat                             Start interactive chat
  catpaw --configs /etc/catpaw/conf.d chat   Use custom config directory`)
}

func printDiagnoseUsage() {
	fmt.Println(`Usage: catpaw diagnose <command>

View and manage past diagnosis and inspection records.

Commands:
  list          List recent records (up to 50)
  show <id>     Show full details of a specific record

Examples:
  catpaw diagnose list
  catpaw diagnose show alert_redis_10_0_0_1_6379_1709312345678`)
}

func printInspectUsage() {
	fmt.Println(`Usage: catpaw inspect <plugin> [target]

Run a proactive AI-powered health inspection against a target.
For remote plugins (redis, mysql), target is required.
For local plugins (cpu, mem, disk), target defaults to localhost.

Examples:
  catpaw inspect redis 10.0.0.1:6379   Inspect a remote Redis instance
  catpaw inspect cpu                    Inspect local CPU status
  catpaw inspect mem                    Inspect local memory status
  catpaw inspect disk                   Inspect local disk status
  catpaw inspect system                 Full local system inspection

The inspection result is saved as a record. Use 'catpaw diagnose list'
to view past inspections and diagnoses.`)
}

func printSelftestUsage() {
	fmt.Println(`Usage: catpaw selftest [filter] [-q]

Smoke-test all registered diagnostic tools on the current machine.
Each local tool is executed with safe default parameters. Remote tools
(requiring a network connection) are skipped.

No AI API is needed. No system state is modified.

Options:
  filter      Only test categories matching this string (e.g. sysdiag, cpu)
  -q          Quiet mode: only show failures and summary

Exit code:
  0           All tests passed (or skipped/warned)
  1           One or more tests failed

Examples:
  catpaw selftest                  Test all tools
  catpaw selftest sysdiag          Test only sysdiag tools
  catpaw selftest -q               Quiet mode`)
}
