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
	configDir   = flag.String("configs", "conf.d", "Specify configuration directory.")
	testMode    = flag.Bool("test", false, "Is test mode? Print results to stdout if --test given.")
	interval    = flag.Int64("interval", 0, "Global interval(unit:Second).")
	showVersion = flag.Bool("version", false, "Show version.")
	plugins     = flag.String("plugins", "", "e.g. plugin1:plugin2")
	url         = flag.String("url", "", "e.g. https://api.flashcat.cloud/event/push/alert/standard?integration_key=x")
	loglevel    = flag.String("loglevel", "", "e.g. debug, info, warn, error, fatal")
)

func init() {
	var err error
	if appPath, err = winsvc.GetAppPath(); err != nil {
		panic(err)
	}
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Println(config.Version)
		os.Exit(0)
	}

	if handleSubcommand(flag.Args()) {
		return
	}

	winx.Args(appPath)

	if err := config.InitConfig(*configDir, *testMode, *interval, *plugins, *url, *loglevel); err != nil {
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
	if len(args) == 0 || args[0] != "diagnose" {
		return false
	}

	stateDir := filepath.Join(filepath.Dir(*configDir), "state.d")

	if len(args) < 2 {
		printDiagnoseUsage()
		return true
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
	return true
}

func printDiagnoseUsage() {
	fmt.Println("Usage: catpaw diagnose <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list          List recent diagnosis records")
	fmt.Println("  show <id>     Show full details of a diagnosis record")
}
