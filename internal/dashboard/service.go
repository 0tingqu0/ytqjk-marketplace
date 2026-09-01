package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errServiceNotConfigured = errors.New("dashboard service is not configured")

type serviceSpec struct {
	Binary        string
	KnowledgeRoot string
	Assets        string
	Port          int
}

type ServiceStatus struct {
	Status    string `json:"status"`
	Changed   bool   `json:"changed"`
	Port      int    `json:"port"`
	Autostart string `json:"autostart"`
	PID       int    `json:"pid,omitempty"`
}

func StartService(binary, knowledgeRoot, assets string, port int) ServiceStatus {
	configured := ConfigureService(binary, knowledgeRoot, assets, port)
	if configured.Status == "FAILED" {
		return configured
	}
	if running := Probe(port); running.Status == "RUNNING" {
		if !configured.Changed {
			running.Autostart = configured.Autostart
			return running
		}
		if _, _, err := stopPlatformService(); err != nil {
			return ServiceStatus{Status: "FAILED", Port: port, Autostart: configured.Autostart}
		}
	}
	started := StartConfiguredService(port)
	started.Changed = configured.Changed || started.Changed
	return started
}

func ConfigureService(binary, knowledgeRoot, assets string, port int) ServiceStatus {
	spec, err := prepareServiceSpec(binary, knowledgeRoot, assets, port)
	if err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	changed, autostart, err := configurePlatformService(spec)
	if err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	return ServiceStatus{Status: "CONFIGURED", Changed: changed, Port: port, Autostart: autostart}
}

func StartConfiguredService(port int) ServiceStatus {
	if err := startPlatformService(); err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: "UNKNOWN"}
	}
	for attempt := 0; attempt < 40; attempt++ {
		time.Sleep(100 * time.Millisecond)
		if status := Probe(port); status.Status == "RUNNING" {
			status.Changed = true
			status.Autostart = platformAutostart()
			return status
		}
	}
	return ServiceStatus{Status: "FAILED", Port: port, Autostart: platformAutostart()}
}

func StopService(knowledgeRoot string, port int) ServiceStatus {
	changed, autostart, err := stopPlatformService()
	return waitForServiceStop(port, changed, autostart, err)
}

func RemoveService(knowledgeRoot string, port int) ServiceStatus {
	changed, autostart, err := removePlatformService()
	return waitForServiceStop(port, changed, autostart, err)
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

func prepareServiceSpec(binary, knowledgeRoot, assets string, port int) (serviceSpec, error) {
	if port < 1 || port > 65535 || strings.ContainsAny(binary+knowledgeRoot+assets, "\x00\r\n") {
		return serviceSpec{}, errors.New("dashboard service parameters are invalid")
	}
	values := []*string{&binary, &knowledgeRoot, &assets}
	for _, value := range values {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return serviceSpec{}, err
		}
		*value = filepath.Clean(absolute)
	}
	if err := os.MkdirAll(knowledgeRoot, 0o700); err != nil {
		return serviceSpec{}, err
	}
	for path, directory := range map[string]bool{binary: false, knowledgeRoot: true, assets: true} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory {
			return serviceSpec{}, errors.Join(errors.New("dashboard service path is unsafe"), err)
		}
	}
	indexInfo, err := os.Lstat(filepath.Join(assets, "index.html"))
	if err != nil || !indexInfo.Mode().IsRegular() || indexInfo.Mode()&os.ModeSymlink != 0 {
		return serviceSpec{}, errors.Join(errors.New("dashboard assets are incomplete"), err)
	}
	return serviceSpec{Binary: binary, KnowledgeRoot: knowledgeRoot, Assets: assets, Port: port}, nil
}

func waitForServiceStop(port int, changed bool, autostart string, err error) ServiceStatus {
	if errors.Is(err, errServiceNotConfigured) && Probe(port).Status != "RUNNING" {
		return ServiceStatus{Status: "STOPPED", Port: port, Autostart: "NOT_CONFIGURED"}
	}
	if err != nil {
		return ServiceStatus{Status: "FAILED", Port: port, Autostart: autostart}
	}
	for attempt := 0; attempt < 40; attempt++ {
		if Probe(port).Status != "RUNNING" {
			return ServiceStatus{Status: "STOPPED", Changed: changed, Port: port, Autostart: autostart}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ServiceStatus{Status: "FAILED", Port: port, Autostart: autostart}
}
