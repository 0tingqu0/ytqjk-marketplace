import { errorFrom } from "./js/api.js";

(() => {
  const panel = document.getElementById("update-panel");
  const status = document.getElementById("update-status");
  const button = document.getElementById("install-update");
  const trigger = document.getElementById("version-trigger");
  let token = "";
  let latestVersion = "";

  function setPanelOpen(open) {
    panel.hidden = !open;
    trigger.setAttribute("aria-expanded", String(open));
  }

  async function recoverCompletedUpdate(expectedVersion) {
    for (let attempt = 0; attempt < 60; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 500));
      try {
        const response = await fetch("/api/update", { cache: "no-store" });
        const result = await response.json();
        if (String(result.current_version || "") === expectedVersion) {
          return {
            status: "UPDATED",
            latest_version: expectedVersion,
            restart_required: true,
          };
        }
      } catch (_error) {
        // The local service may still be restarting.
      }
    }
    return null;
  }

  async function checkUpdate() {
    const response = await fetch("/api/update", { cache: "no-store" });
    const result = await response.json();
    token = typeof result.token === "string" ? result.token : "";
    const currentVersion = String(result.current_version || "");
    trigger.textContent = currentVersion ? `v${currentVersion}` : "v–";
    if (!response.ok) {
      throw new Error(errorFrom(result, "检查更新失败"));
    }
    latestVersion = String(result.latest_version || "");
    const updateAvailable = Boolean(result.update_available);
    trigger.classList.toggle("has-update", updateAvailable);
    trigger.setAttribute(
      "aria-label",
      updateAvailable
        ? `当前版本 v${currentVersion}，发现 v${latestVersion}，点击查看更新`
        : `当前版本 v${currentVersion}，点击查看版本信息`,
    );
    if (updateAvailable) {
      status.textContent = `发现 v${latestVersion}`;
      button.hidden = false;
      button.disabled = false;
    } else {
      status.textContent = currentVersion
        ? `v${currentVersion} 已是最新版本`
        : "当前已是最新版本";
      button.hidden = true;
    }
  }

  async function postUpdate(retryToken = true) {
    let response;
    let result;
    try {
      response = await fetch("/api/update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      result = await response.json();
    } catch (error) {
      const recovered = await recoverCompletedUpdate(latestVersion);
      if (recovered) return recovered;
      throw error;
    }
    if (
      !response.ok
      && retryToken
      && result.error_code === "UPDATE_TOKEN_INVALID"
    ) {
      await checkUpdate();
      button.disabled = true;
      status.textContent = "更新中，请勿关闭";
      return postUpdate(false);
    }
    if (!response.ok) throw new Error(errorFrom(result, "更新失败"));
    return result;
  }

  async function installUpdate() {
    if (!token || !latestVersion) return;
    if (!confirm(`更新 YTQJK 到 v${latestVersion}？知识库数据不会删除。`)) return;
    button.disabled = true;
    status.textContent = "更新中，请勿关闭";
    try {
      let result = await postUpdate();
      if (result.status === "UPDATING") {
        result = await recoverCompletedUpdate(latestVersion);
        if (!result) {
          throw new Error("升级未在预期时间内完成，请检查升级状态或执行回滚");
        }
      }
      latestVersion = String(result.latest_version || latestVersion);
      status.textContent = result.status === "UPDATED"
        ? `已更新至 v${latestVersion}，重启 Codex 生效`
        : `当前已是 v${latestVersion}`;
      trigger.textContent = `v${latestVersion}`;
      trigger.classList.remove("has-update");
      trigger.setAttribute(
        "aria-label",
        `当前版本 v${latestVersion}，点击查看版本信息`,
      );
      button.hidden = true;
      token = "";
    } catch (error) {
      status.textContent = error instanceof Error ? error.message : "更新失败";
      button.disabled = false;
    }
  }

  trigger.addEventListener("click", () => setPanelOpen(panel.hidden));
  button.addEventListener("click", installUpdate);
  document.addEventListener("click", (event) => {
    const clickedOutside = event.target !== trigger
      && !panel.contains(event.target);
    if (!panel.hidden && clickedOutside) {
      setPanelOpen(false);
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !panel.hidden) {
      setPanelOpen(false);
      trigger.focus();
    }
  });
  checkUpdate().catch(() => {
    status.textContent = "暂时无法检查更新";
    button.hidden = true;
  });
  setInterval(() => checkUpdate().catch(() => undefined), 60 * 60 * 1000);
})();
