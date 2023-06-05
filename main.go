package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/winx"
	"github.com/chai2010/winsvc"
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
	// change to current dir
	var err error
	if appPath, err = winsvc.GetAppPath(); err != nil {
		panic(err)
	}
	if err := os.Chdir(filepath.Dir(appPath)); err != nil {
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

	logger.Debug("Debug: Hello from zap logger")
	logger.Info("Info: Hello from zap logger")
	logger.Warn("Warn: Hello from zap logger")
}
