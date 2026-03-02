package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/chai2010/winsvc"
	"github.com/cprobe/catpaw/agent"
	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/winx"
	"github.com/toolkits/pkg/runner"
)

var (
	appPath     string
	configDir   = flag.String("configs", "conf.d", "Configuration directory")
	testMode    = flag.Bool("test", false, "Test mode: print results to stdout")
	interval    = flag.Int64("interval", 0, "Global collection interval (seconds)")
	showVersion = flag.Bool("version", false, "Show version")
	plugins     = flag.String("plugins", "", "Plugin filter (e.g. redis:cpu:disk)")
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

	if handleSubcommand(args) {
		return
	}

	winx.Args(appPath)

	if err := config.InitConfig(*configDir, *testMode, *interval, *plugins, *loglevel); err != nil {
		panic(err)
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

// handleSubcommand handles CLI subcommands that don't require the full agent.
// Returns true if a subcommand was handled (caller should exit).
func handleSubcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "diagnose":
		handleDiagnoseSubcommand(args)
		return true
	case "inspect":
		handleInspectSubcommand(args)
		return true
	default:
		return false
	}
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

func printUsage() {
	fmt.Fprintf(os.Stderr, `catpaw %s - Lightweight monitoring agent with AI-powered diagnostics

Usage:
  catpaw [flags]                          Start the monitoring agent
  catpaw inspect <plugin> [target]        Run health inspection on a target
  catpaw diagnose <command>               Manage diagnosis records
  catpaw help [command]                   Show help for a command

Flags:
`, config.Version)
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, `
Commands:
  inspect     Proactive health inspection (AI-powered)
  diagnose    View past diagnosis / inspection records

Run 'catpaw help <command>' for details on a specific command.`)
}

func printSubcommandHelp(cmd string) {
	switch cmd {
	case "inspect":
		printInspectUsage()
	case "diagnose":
		printDiagnoseUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %q\n\n", cmd)
		printUsage()
	}
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
