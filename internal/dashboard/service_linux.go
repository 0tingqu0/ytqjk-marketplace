//go:build linux

package dashboard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const dashboardUnitName = "ytqjk-dashboard.service"

func platformAutostart() string { return "SYSTEMD_USER" }

func configurePlatformService(spec serviceSpec) (bool, string, error) {
	path, err := dashboardUnitPath()
	if err != nil {
		return false, "SYSTEMD_USER", err
	}
	data := []byte(systemdUnit(spec))
	current, readErr := os.ReadFile(path)
	changed := readErr != nil || !bytes.Equal(current, data)
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return false, "SYSTEMD_USER", err
		}
		if err := safeio.AtomicWrite(path, data, 0o600); err != nil {
			return false, "SYSTEMD_USER", err
		}
		if err := runSystemctl("daemon-reload"); err != nil {
			return false, "SYSTEMD_USER", err
		}
		if err := runSystemctl("enable", dashboardUnitName); err != nil {
			return false, "SYSTEMD_USER", err
		}
	}
	return changed, "SYSTEMD_USER", nil
}

func startPlatformService() error {
	return runSystemctl("start", dashboardUnitName)
}

func stopPlatformService() (bool, string, error) {
	path, err := dashboardUnitPath()
	if err != nil {
		return false, "SYSTEMD_USER", err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, "NOT_CONFIGURED", errServiceNotConfigured
	}
	if err := runSystemctl("stop", dashboardUnitName); err != nil {
		return false, "SYSTEMD_USER", err
	}
	return true, "SYSTEMD_USER", nil
}

func removePlatformService() (bool, string, error) {
	path, err := dashboardUnitPath()
	if err != nil {
		return false, "SYSTEMD_USER", err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, "NOT_CONFIGURED", errServiceNotConfigured
	}
	if err := runSystemctl("disable", "--now", dashboardUnitName); err != nil {
		return false, "SYSTEMD_USER", err
	}
	if err := os.Remove(path); err != nil {
		return false, "SYSTEMD_USER", err
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return false, "SYSTEMD_USER", err
	}
	return true, "REMOVED", nil
}

func dashboardUnitPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "systemd", "user", dashboardUnitName), nil
}

func systemdUnit(spec serviceSpec) string {
	arguments := []string{
		systemdQuote(spec.Binary), "dashboard", "serve", "--knowledge-root", systemdQuote(spec.KnowledgeRoot),
		"--assets", systemdQuote(spec.Assets), "--port", strconv.Itoa(spec.Port),
	}
	return fmt.Sprintf(`[Unit]
Description=YTQJK Dashboard
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
UMask=0077

[Install]
WantedBy=default.target
`, strings.Join(arguments, " "))
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, `%`, `%%`)
	return `"` + value + `"`
}

func runSystemctl(arguments ...string) error {
	command := exec.Command("systemctl", append([]string{"--user"}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}
