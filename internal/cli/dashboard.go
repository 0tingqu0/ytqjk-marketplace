package cli

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
)

func (context commandContext) dashboard(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "serve", "start", "stop", "status", "restart")
	if err != nil {
		return err
	}
	flags := quietFlags("dashboard " + command)
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	assetsValue := flags.String("assets", "", "dashboard asset directory")
	port := flags.Int("port", dashboard.DefaultPort, "loopback port")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		return err
	}
	if command == "status" {
		return context.write(dashboard.Probe(*port))
	}
	if command == "stop" {
		return context.write(dashboard.StopService(knowledgeRoot, *port))
	}
	assets, err := resolveAssets(*assetsValue, "dashboard")
	if err != nil {
		return err
	}
	if command == "serve" {
		logger := log.New(context.errOut, "ytqjk-dashboard ", log.LstdFlags|log.LUTC)
		return dashboard.Run(knowledgeRoot, assets, *port, logger)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if command == "restart" {
		status := dashboard.StopService(knowledgeRoot, *port)
		if status.Status == "FAILED" {
			return errors.New("dashboard stop failed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return context.write(dashboard.StartService(binary, knowledgeRoot, assets, *port))
}

func resolveAssets(explicit, kind string) (string, error) {
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if executable, err := os.Executable(); err == nil {
		pluginRoot := filepath.Dir(filepath.Dir(executable))
		if kind == "dashboard" {
			candidates = append(candidates, filepath.Join(pluginRoot, "skills", "ytqjk", "dashboard"))
		} else {
			candidates = append(candidates, filepath.Join(pluginRoot, "skills", "ytqjk-knowledge", "workbench"))
		}
	}
	if source, err := platform.SourceRoot(""); err == nil {
		if kind == "dashboard" {
			candidates = append(candidates, filepath.Join(source, "plugins", "ytqjk-agentic-orchestrator", "skills", "ytqjk", "dashboard"))
		} else {
			candidates = append(candidates, filepath.Join(source, "plugins", "ytqjk-knowledge", "skills", "ytqjk-knowledge", "workbench"))
		}
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, err := os.Stat(filepath.Join(absolute, "index.html")); err == nil && info.Mode().IsRegular() {
			return absolute, nil
		}
	}
	return "", errors.New(kind + " assets could not be located; use --assets")
}
