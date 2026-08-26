export async function refreshAfterSuccess(refresh, report, message) {
  try {
    await refresh();
  } catch {
    report(`${message}；列表刷新失败，请点击刷新`);
  }
}
