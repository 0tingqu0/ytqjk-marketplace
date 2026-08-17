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

  async function checkUpdate() {
    const response = await fetch("/api/update", { cache: "no-store" });
    if (!response.ok) throw new Error("检查更新失败");
    const result = await response.json();
    token = typeof result.token === "string" ? result.token : "";
    const currentVersion = String(result.current_version || "");
    latestVersion = String(result.latest_version || "");
    const updateAvailable = Boolean(result.update_available);
    trigger.textContent = currentVersion ? `v${currentVersion}` : "v–";
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

  async function installUpdate() {
    if (!token || !latestVersion) return;
    if (!confirm(`更新 YTQJK 到 v${latestVersion}？知识库数据不会删除。`)) return;
    button.disabled = true;
    status.textContent = "更新中，请勿关闭";
    try {
      const response = await fetch("/api/update", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || "更新失败");
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
