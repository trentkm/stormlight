import type { Agent, Provider, Workspace } from "./types";

// The token is shell access to every workspace the catalog knows. It
// arrives in the URL because that is the only way `stormlight serve` can
// hand it to a browser, and it comes straight out of the address bar so
// it is not sitting in a title bar or a screenshot.
//
// It is kept in sessionStorage rather than a variable, because a reload
// with only a variable is a lockout: the URL no longer carries it and the
// page has nowhere to type one. sessionStorage is scoped to this tab and
// dies with it, which is the lifetime the token already had — localStorage
// would instead leave it on disk long after the server that minted it.
const tokenKey = "stormlight.token";

let token = sessionStorage.getItem(tokenKey) ?? "";

export function claimToken(): boolean {
  const url = new URL(window.location.href);
  const found = url.searchParams.get("token");
  if (found) {
    token = found;
    sessionStorage.setItem(tokenKey, token);
    url.searchParams.delete("token");
    window.history.replaceState({}, "", url.toString());
  }
  return token !== "";
}

/** forgetToken drops a token the server no longer accepts. */
export function forgetToken(): void {
  token = "";
  sessionStorage.removeItem(tokenKey);
}

export function tokened(path: string, params: Record<string, string> = {}): string {
  const url = new URL(path, window.location.origin);
  url.searchParams.set("token", token);
  for (const [key, value] of Object.entries(params)) {
    url.searchParams.set(key, value);
  }
  return url.toString();
}

/** socketURL is tokened(), addressed for a WebSocket. */
export function socketURL(path: string, params: Record<string, string> = {}): string {
  const url = new URL(tokened(path, params));
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(tokened(path), {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    // Every failure has the same shape, so there is one thing to parse.
    const detail = await response.json().catch(() => ({ error: response.statusText }));
    throw new APIError(response.status, detail.error ?? response.statusText);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

export const api = {
  agents: () => call<Agent[]>("GET", "/api/agents"),
  workspaces: () => call<Workspace[]>("GET", "/api/workspaces"),
  providers: () => call<Provider[]>("GET", "/api/providers"),

  dispatch: (request: {
    provider: string;
    task: string;
    cwd: string;
    name?: string;
    host?: string;
    mode?: string;
  }) => call<Agent>("POST", "/api/agents", request),

  send: (id: string, message: string) =>
    call<void>("POST", `/api/agents/${id}/message`, { message }),
  interrupt: (id: string) => call<void>("POST", `/api/agents/${id}/interrupt`),
  clearAttention: (id: string) =>
    call<void>("POST", `/api/agents/${id}/clear-attention`),
  rename: (id: string, name: string) =>
    call<void>("PATCH", `/api/agents/${id}`, { name }),
  remove: (id: string) => call<void>("DELETE", `/api/agents/${id}`),

  addWorkspace: (path: string, host = "") =>
    call<Workspace>("POST", "/api/workspaces", { path, host }),
  removeWorkspace: (workspace: Workspace) =>
    call<void>("DELETE", "/api/workspaces", { workspace }),
};

/**
 * roster pushes the whole roster whenever it changes. The server never
 * expects a reply, so the only thing to handle is the socket going away:
 * reconnect, and it sends the current roster again on connect.
 */
export function roster(onRoster: (agents: Agent[]) => void): () => void {
  let socket: WebSocket | null = null;
  let retry: number | undefined;
  let stopped = false;

  const connect = () => {
    if (stopped) return;
    socket = new WebSocket(socketURL("/api/events"));
    socket.onmessage = (event) => {
      const payload = JSON.parse(event.data);
      if (payload.type === "agents") {
        onRoster(payload.agents ?? []);
      }
    };
    socket.onclose = () => {
      if (stopped) return;
      // The server is local; a close means it restarted or the tab slept.
      retry = window.setTimeout(connect, 1000);
    };
  };
  connect();

  return () => {
    stopped = true;
    window.clearTimeout(retry);
    socket?.close();
  };
}
