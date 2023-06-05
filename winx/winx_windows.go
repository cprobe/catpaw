//go:build windows

package winx

import (
	"flag"
	"fmt"
	"os"

	"github.com/chai2010/winsvc"
)

var (
	flagWinSvcName      = flag.String("win-service-name", "categraf", "Set windows service name")
	flagWinSvcDesc      = flag.String("win-service-desc", "Categraf", "Set windows service description")
	flagWinSvcInstall   = flag.Bool("win-service-install", false, "Install windows service")
	flagWinSvcUninstall = flag.Bool("win-service-uninstall", false, "Uninstall windows service")
	flagWinSvcStart     = flag.Bool("win-service-start", false, "Start windows service")
	flagWinSvcStop      = flag.Bool("win-service-stop", false, "Stop windows service")
)

func GetServiceName() string {
	return *flagWinSvcName
}

func Args(appPath string) {
	// install service
	if *flagWinSvcInstall {
		if err := winsvc.InstallService(appPath, *flagWinSvcName, *flagWinSvcDesc); err != nil {
			fmt.Println("failed to install service:", *flagWinSvcName, "error:", err)
		}
		fmt.Println("done")
		os.Exit(0)
	}

	// uninstall service
	if *flagWinSvcUninstall {
		if err := winsvc.RemoveService(*flagWinSvcName); err != nil {
			fmt.Println("failed to uninstall service:", *flagWinSvcName, "error:", err)
		}
		fmt.Println("done")
		os.Exit(0)
	}

	// start service
	if *flagWinSvcStart {
		if err := winsvc.StartService(*flagWinSvcName); err != nil {
			fmt.Println("failed to start service:", *flagWinSvcName, "error:", err)
		}
		fmt.Println("done")
		os.Exit(0)
	}

	// stop service
	if *flagWinSvcStop {
		if err := winsvc.StopService(*flagWinSvcName); err != nil {
			fmt.Println("failed to stop service:", *flagWinSvcName, "error:", err)
		}
		fmt.Println("done")
		os.Exit(0)
	}
}
