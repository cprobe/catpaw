//go:build !windows

package winx

func Args(appPath string) {
}

func GetServiceName() string {
	return ""
}
