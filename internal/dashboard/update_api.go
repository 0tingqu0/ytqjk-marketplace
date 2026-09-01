package dashboard

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

type updateBackend interface {
	Check(context.Context, string) (upgrade.CheckResult, upgrade.Release, error)
	Prepare(context.Context, upgrade.Release, upgrade.PrepareOptions) (upgrade.Plan, error)
	Launch(upgrade.Plan, int) error
}

type goUpdateBackend struct{ client *upgrade.Client }

func (backend goUpdateBackend) Check(ctx context.Context, current string) (upgrade.CheckResult, upgrade.Release, error) {
	return backend.client.Check(ctx, current)
}

func (backend goUpdateBackend) Prepare(ctx context.Context, release upgrade.Release, options upgrade.PrepareOptions) (upgrade.Plan, error) {
	return upgrade.Prepare(ctx, backend.client, release, options)
}

func (backend goUpdateBackend) Launch(plan upgrade.Plan, parentPID int) error {
	return upgrade.Launch(plan, parentPID)
}

func (s *Server) updateStatus(writer http.ResponseWriter, request *http.Request) int {
	token, backend, err := s.updateDependencies()
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "UPDATE_STATE_UNAVAILABLE", "更新状态不可用")
		return http.StatusServiceUnavailable
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	check, _, err := backend.Check(ctx, buildinfo.Version)
	runtimeRoot, runtimeErr := platform.RuntimeRoot()
	state := upgrade.State{Status: "UNKNOWN", CurrentVersion: buildinfo.Version}
	if runtimeErr == nil {
		state = upgrade.Status(runtimeRoot, buildinfo.Version)
	}
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{
			"ok": false, "error": "无法读取 GitHub 最新稳定版本。", "error_code": "UPDATE_CHECK_FAILED",
			"current_version": buildinfo.Version, "token": token, "state": state.Status,
			"rollback_available": upgrade.CanRollback(state, buildinfo.Version),
		})
		return http.StatusBadGateway
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "current_version": check.CurrentVersion, "latest_version": check.LatestVersion,
		"update_available": check.UpdateAvailable, "release_url": check.ReleaseURL, "token": token,
		"state": state.Status, "upgrade_mode": "transactional-snapshot",
		"rollback_available": upgrade.CanRollback(state, buildinfo.Version),
	})
	return http.StatusOK
}

func (s *Server) startUpdate(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Token string `json:"token"`
	}
	if err := readJSON(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "UPDATE_REQUEST_INVALID", "更新请求无效。")
		return http.StatusBadRequest
	}
	token, backend, err := s.updateDependencies()
	if err != nil || subtle.ConstantTimeCompare([]byte(payload.Token), []byte(token)) != 1 {
		writeJSON(writer, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "更新请求无效。", "error_code": "UPDATE_TOKEN_INVALID",
		})
		return http.StatusBadRequest
	}
	if !s.updateMu.TryLock() {
		writeError(writer, http.StatusConflict, "UPDATE_BUSY", "更新正在进行。")
		return http.StatusConflict
	}
	defer s.updateMu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 4*time.Minute)
	defer cancel()
	check, release, err := backend.Check(ctx, buildinfo.Version)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "UPDATE_CHECK_FAILED", "无法读取 GitHub 最新稳定版本。")
		return http.StatusBadGateway
	}
	if !check.UpdateAvailable {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "status": "UP_TO_DATE", "current_version": buildinfo.Version,
			"latest_version": check.LatestVersion, "restart_required": false,
		})
		return http.StatusOK
	}
	runtimeRoot, err := platform.RuntimeRoot()
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "UPDATE_PATHS_UNAVAILABLE", "升级目录不可用。")
		return http.StatusServiceUnavailable
	}
	codexRoot, err := platform.CodexRoot("")
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "UPDATE_PATHS_UNAVAILABLE", "升级目录不可用。")
		return http.StatusServiceUnavailable
	}
	plan, err := backend.Prepare(ctx, release, upgrade.PrepareOptions{
		RuntimeRoot: runtimeRoot, CodexRoot: codexRoot, KnowledgeRoot: s.KnowledgeRoot,
		CurrentVersion: buildinfo.Version, Port: s.Port, RestartDashboard: true,
	})
	if err != nil {
		writeError(writer, http.StatusBadGateway, upgradeErrorCode(err), "更新准备失败，当前版本未变更。")
		return http.StatusBadGateway
	}
	if err := backend.Launch(plan, os.Getpid()); err != nil {
		writeError(writer, http.StatusInternalServerError, upgradeErrorCode(err), "无法启动升级切换进程。")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"ok": true, "status": "UPDATING", "current_version": buildinfo.Version,
		"latest_version": release.Version, "restart_required": true,
		"upgrade_mode": "transactional-snapshot",
	})
	s.shutdownAfterResponse()
	return http.StatusAccepted
}

func (s *Server) updateDependencies() (string, updateBackend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateToken == "" {
		token, err := safeio.RandomHex(32)
		if err != nil {
			return "", nil, err
		}
		s.updateToken = token
	}
	if s.updates == nil {
		s.updates = goUpdateBackend{client: upgrade.NewClient()}
	}
	return s.updateToken, s.updates, nil
}

func (s *Server) shutdownAfterResponse() {
	if s.server == nil {
		return
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	}()
}

func upgradeErrorCode(err error) string {
	var value *upgrade.Error
	if errors.As(err, &value) {
		return value.Code
	}
	return "UPDATE_FAILED"
}
