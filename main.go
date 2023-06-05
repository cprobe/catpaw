package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"flashcat.cloud/catpaw/agent"
	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/duty"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/winx"
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

	if err := config.InitConfig(*configDir, *testMode, *interval, *plugins); err != nil {
		panic(err)
	}

	logger := logger.Build().Sugar()
	defer logger.Sync()

	runner.Init()
	logger.Info("runner.binarydir: ", runner.Cwd)
	logger.Info("runner.configdir: ", *configDir)
	logger.Info("runner.hostname: ", runner.Hostname)
	logger.Info("runner.fd_limits: ", runner.FdLimits())
	logger.Info("runner.vm_limits: ", runner.VMLimits())

	dutyClient := duty.NewDutyClient(logger)
	dutyClient.Start()

	agent := agent.NewAgent(logger, dutyClient)

	if runtime.GOOS == "windows" && !winsvc.IsAnInteractiveSession() {
		if err := winsvc.RunAsService(winx.GetServiceName(), agent.Start, agent.Stop, false); err != nil {
			fmt.Println("failed to run windows service:", err)
			os.Exit(1)
		}
		return
	} else {
		agent.Start()
	}

	sc := make(chan os.Signal, 1)
	// syscall.SIGUSR2 == 0xc , not available on windows
	signal.Notify(sc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGPIPE)

EXIT:
	for {
		sig := <-sc
		logger.Info("received signal: ", sig.String())
		switch sig {
		case syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT:
			break EXIT
		case syscall.SIGHUP:
			agent.Reload()
		case syscall.SIGPIPE:
			// https://pkg.go.dev/os/signal#hdr-SIGPIPE
			// do nothing
		}
	}

	agent.Stop()
	logger.Info("agent exited")
}
