(() => {
  const panel = document.getElementById("update-panel");
  const status = document.getElementById("update-status");
  const button = document.getElementById("install-update");
  let token = "";
  let latestVersion = "";

  async function checkUpdate() {
    const response = await fetch("/api/update", { cache: "no-store" });
    if (!response.ok) throw new Error("检查更新失败");
    const result = await response.json();
    token = typeof result.token === "string" ? result.token : "";
    latestVersion = String(result.latest_version || "");
    panel.hidden = !result.update_available;
    if (result.update_available) {
      status.textContent = `发现 v${latestVersion}`;
      button.hidden = false;
      button.disabled = false;
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
      button.hidden = true;
    } catch (error) {
      status.textContent = error.message || "更新失败";
      button.disabled = false;
    }
  }

  button.addEventListener("click", installUpdate);
  checkUpdate().catch(() => { panel.hidden = true; });
  setInterval(() => checkUpdate().catch(() => undefined), 60 * 60 * 1000);
})();
