export function selectionFor(state, item) {
  if (state.selected?.item.path === item.path) return state.selected;
  if (state.reviewSelected?.item.path === item.path) {
    return state.reviewSelected;
  }
  return null;
}

export async function detectDraftConflicts(api, state) {
  const selections = [state.selected, state.reviewSelected].filter(Boolean);
  let conflicts = 0;
  await Promise.all(selections.map(async (selection) => {
    const path = selection.item.path;
    if (!state.drafts.has(path) || !selection.version) return;
    try {
      const current = await api.document(path);
      if (current.version !== selection.version) {
        selection.conflict = "资料已被其他会话更新；草稿未覆盖，请重新载入后合并。";
        conflicts += 1;
      }
    } catch {
      // The primary refresh path reports connectivity failures.
    }
  }));
  return conflicts;
}
