package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type ServiceStatus struct {
	Status    string `json:"status"`
	Changed   bool   `json:"changed"`
	Port      int    `json:"port"`
	Autostart string `json:"autostart"`
	PID       int    `json:"pid,omitempty"`
}

func StartService(binary, knowledgeRoot, assets string, port int) ServiceStatus {
	if status := Probe(port); status.Status == "RUNNING" {
		return status
	}
	if err := os.MkdirAll(knowledgeRoot, 0o755); err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	logPath := filepath.Join(knowledgeRoot, "dashboard.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	command := exec.Command(binary, "dashboard", "serve", "--knowledge-root", knowledgeRoot, "--assets", assets, "--port", strconv.Itoa(port))
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	configureDetached(command)
	if err := command.Start(); err != nil {
		logFile.Close()
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	_ = logFile.Close()
	for attempt := 0; attempt < 40; attempt++ {
		time.Sleep(100 * time.Millisecond)
		if status := Probe(port); status.Status == "RUNNING" {
			status.Changed = true
			status.PID = pid
			status.Autostart = "PROCESS"
			return status
		}
	}
	return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED", PID: pid}
}

func StopService(knowledgeRoot string, port int) ServiceStatus {
	pidData, err := os.ReadFile(filepath.Join(knowledgeRoot, "dashboard.pid"))
	if err != nil {
		if Probe(port).Status != "RUNNING" {
			return ServiceStatus{Status: "STOPPED", Port: port, Autostart: "NOT_CONFIGURED"}
		}
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN"}
	}
	pid, err := strconv.Atoi(string(bytesTrimSpace(pidData)))
	if err != nil || pid <= 0 {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN"}
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Kill() != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN", PID: pid}
	}
	_ = os.Remove(filepath.Join(knowledgeRoot, "dashboard.pid"))
	return ServiceStatus{Status: "STOPPED", Changed: true, Port: port, Autostart: "REMOVED", PID: pid}
}

func Probe(port int) ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/health", port), nil)
	request.Host = fmt.Sprintf("127.0.0.1:%d", port)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ServiceStatus{Status: "STOPPED", Port: port, Autostart: "UNKNOWN"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN"}
	}
	var body map[string]any
	if json.NewDecoder(response.Body).Decode(&body) != nil || body["status"] != "RUNNING" {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN"}
	}
	return ServiceStatus{Status: "RUNNING", Port: port, Autostart: "UNKNOWN"}
}

func bytesTrimSpace(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
