export const ROUTES = {
  overview: ["状态", "总览"],
  intake: ["投递", "投递任务"],
  documents: ["检索", "知识文档"],
  review: ["人工决策", "候选审阅"],
  libraries: ["项目层", "知识库树"],
  sessions: ["会话层", "会话锚点"],
};

function routeFromHash() {
  const route = window.location.hash.slice(1);
  return Object.hasOwn(ROUTES, route) ? route : "overview";
}

export function createRouter(onRoute) {
  let currentRoute = null;
  const apply = () => {
    const route = routeFromHash();
    const changed = currentRoute !== null && currentRoute !== route;
    currentRoute = route;
    onRoute(route, changed);
    if (changed) {
      window.scrollTo({ top: 0, left: 0, behavior: "auto" });
    }
  };
  window.addEventListener("hashchange", apply);
  if (!Object.hasOwn(ROUTES, window.location.hash.slice(1))) {
    history.replaceState(null, "", "#overview");
  }
  apply();
  return {
    go(route) {
      if (!Object.hasOwn(ROUTES, route)) return;
      if (routeFromHash() === route) onRoute(route, false);
      else window.location.hash = route;
    },
  };
}
