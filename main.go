package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/cprobe/catpaw/agent"
	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/winx"
	"github.com/chai2010/winsvc"
	"github.com/toolkits/pkg/runner"
)

var (
	appPath     string
	configDir   = flag.String("configs", "conf.d", "Specify configuration directory.")
	testMode    = flag.Bool("test", false, "Is test mode? Print results to stdout if --test given.")
	interval    = flag.Int64("interval", 0, "Global interval(unit:Second).")
	showVersion = flag.Bool("version", false, "Show version.")
	plugins     = flag.String("plugins", "", "e.g. plugin1:plugin2")
	url      = flag.String("url", "", "e.g. https://api.flashcat.cloud/event/push/alert/standard?integration_key=x")
	loglevel = flag.String("loglevel", "", "e.g. debug, info, warn, error, fatal")
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
		"vm_limits", runner.VMLimits(),
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
