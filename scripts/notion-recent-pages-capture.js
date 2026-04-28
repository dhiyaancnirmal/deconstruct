(async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const readLocalJSON = (key) => {
    try {
      return JSON.parse(localStorage.getItem(key) || "null");
    } catch {
      return null;
    }
  };

  let userId = "";
  let spaceId = "";
  for (let attempt = 0; attempt < 60; attempt += 1) {
    userId = readLocalJSON("LRU:KeyValueStore2:current-user-id")?.value || "";
    spaceId = readLocalJSON("LRU:KeyValueStore2:lastVisitedRouteSpaceId")?.value || "";
    if (userId && spaceId) break;
    await sleep(500);
  }
  if (!userId || !spaceId) {
    throw new Error("Notion user or space id was not available in localStorage");
  }

  const body = {
    spaceId,
    userId,
    limit: 5,
    beforeTimestamp: Date.now(),
    sinceTimestamp: Date.now() - 30 * 24 * 60 * 60 * 1000,
  };
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  const response = await fetch("/api/v3/getRecentPageVisits", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-notion-active-user-header": userId,
      "x-notion-space-id": spaceId,
    },
    body: JSON.stringify(body),
    credentials: "include",
    signal: controller.signal,
  });
  clearTimeout(timeout);
  const text = await response.text();
  return { status: response.status, body, text: text.slice(0, 500) };
})();
