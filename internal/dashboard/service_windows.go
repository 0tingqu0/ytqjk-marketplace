//go:build windows

package dashboard

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const dashboardTaskName = "YTQJK Dashboard"

func platformAutostart() string { return "WINDOWS_TASK" }

func configurePlatformService(spec serviceSpec) (bool, string, error) {
	commandLine := windowsCommandLine(spec)
	current, err := exec.Command("schtasks.exe", "/Query", "/TN", dashboardTaskName, "/XML").Output()
	changed := err != nil || !strings.Contains(string(current), xmlEscape(spec.Binary)) ||
		!strings.Contains(string(current), xmlEscape(commandLineArguments(spec)))
	if !changed {
		return false, "WINDOWS_TASK", nil
	}
	command := exec.Command(
		"schtasks.exe", "/Create", "/F", "/SC", "ONLOGON", "/RL", "LIMITED",
		"/TN", dashboardTaskName, "/TR", commandLine,
	)
	if output, err := command.CombinedOutput(); err != nil {
		return false, "WINDOWS_TASK", errors.New(strings.TrimSpace(string(output)))
	}
	return true, "WINDOWS_TASK", nil
}

func startPlatformService() error {
	output, err := exec.Command("schtasks.exe", "/Run", "/TN", dashboardTaskName).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func stopPlatformService() (bool, string, error) {
	if err := queryWindowsTask(); err != nil {
		return false, "NOT_CONFIGURED", err
	}
	output, err := exec.Command("schtasks.exe", "/End", "/TN", dashboardTaskName).CombinedOutput()
	if err != nil {
		if queryWindowsTask() == nil {
			return false, "WINDOWS_TASK", nil
		}
		return false, "WINDOWS_TASK", errors.New(strings.TrimSpace(string(output)))
	}
	return true, "WINDOWS_TASK", nil
}

func removePlatformService() (bool, string, error) {
	if err := queryWindowsTask(); err != nil {
		return false, "NOT_CONFIGURED", err
	}
	_, _ = exec.Command("schtasks.exe", "/End", "/TN", dashboardTaskName).CombinedOutput()
	output, err := exec.Command("schtasks.exe", "/Delete", "/F", "/TN", dashboardTaskName).CombinedOutput()
	if err != nil {
		return false, "WINDOWS_TASK", errors.New(strings.TrimSpace(string(output)))
	}
	return true, "REMOVED", nil
}

func queryWindowsTask() error {
	if err := exec.Command("schtasks.exe", "/Query", "/TN", dashboardTaskName).Run(); err != nil {
		return errServiceNotConfigured
	}
	return nil
}

func windowsCommandLine(spec serviceSpec) string {
	return syscall.EscapeArg(spec.Binary) + " " + commandLineArguments(spec)
}

func commandLineArguments(spec serviceSpec) string {
	arguments := []string{
		"dashboard", "serve", "--knowledge-root", spec.KnowledgeRoot,
		"--assets", spec.Assets, "--port", strconv.Itoa(spec.Port),
	}
	for index := range arguments {
		arguments[index] = syscall.EscapeArg(arguments[index])
	}
	return strings.Join(arguments, " ")
}

func xmlEscape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	return value
}
