//go:build !windows && !linux

package dashboard

func platformAutostart() string { return "UNSUPPORTED" }

func configurePlatformService(serviceSpec) (bool, string, error) {
	return false, "UNSUPPORTED", errServiceNotConfigured
}

func startPlatformService() error {
	return errServiceNotConfigured
}

func stopPlatformService() (bool, string, error) {
	return false, "UNSUPPORTED", errServiceNotConfigured
}

func removePlatformService() (bool, string, error) {
	return false, "UNSUPPORTED", errServiceNotConfigured
}
