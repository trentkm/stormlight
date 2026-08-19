import type { Agent, Provider, TranscriptEntry, Workspace } from "./types";

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

let token = "";

/**
 * remembered reads what this tab stored, lazily: reading at module scope
 * would make importing this file depend on a browser being present, and
 * anything that imports it — a test, a build-time render — would fail on
 * the import rather than on the call.
 */
function remembered(): string {
  try {
    return sessionStorage.getItem(tokenKey) ?? "";
  } catch {
    // No storage here. The token then lives as long as this page does,
    // which is the behavior a reload used to have anyway.
    return "";
  }
}

function remember(value: string): void {
  try {
    if (value) {
      sessionStorage.setItem(tokenKey, value);
    } else {
      sessionStorage.removeItem(tokenKey);
    }
  } catch {
    // As above: without storage the token is simply not carried across a
    // reload.
  }
}

export function claimToken(): boolean {
  if (!token) token = remembered();
  const url = new URL(window.location.href);
  const found = url.searchParams.get("token");
  if (found) {
    token = found;
    remember(token);
    url.searchParams.delete("token");
    window.history.replaceState({}, "", url.toString());
  }
  return token !== "";
}

/** forgetToken drops a token the server no longer accepts. */
export function forgetToken(): void {
  token = "";
  remember("");
}

export function tokened(path: string, params: Record<string, string> = {}): string {
  if (!token) token = remembered();
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

  transcript: (id: string, after = 0) =>
    call<{ entries: TranscriptEntry[]; total: number; ok: boolean }>(
      "GET",
      `/api/agents/${id}/transcript?after=${after}`,
    ),
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

/** How long to wait before reconnecting the roster, and the ceiling. */
const rosterRetryFloor = 500;
const rosterRetryCeiling = 10_000;

/**
 * roster pushes the whole roster whenever it changes. The server never
 * expects a reply, so the only thing to handle is the socket going away.
 *
 * Which it will: the server is local, and the ordinary reason is that it
 * restarted. That case deserves care rather than a retry loop, because
 * the token is minted per run — the old one is now wrong, and retrying it
 * forever would leave an empty roster, no error, and no hint that the
 * answer is to open the new URL. So a failed reconnect asks the API what
 * it thinks, and a refusal is reported rather than retried.
 */
export function roster(
  onRoster: (agents: Agent[]) => void,
  onLost: (reason: string) => void = () => {},
): () => void {
  let socket: WebSocket | null = null;
  let timer: number | undefined;
  let retry = rosterRetryFloor;
  let stopped = false;

  const connect = () => {
    if (stopped) return;
    const live = new WebSocket(socketURL("/api/events"));
    socket = live;
    live.onopen = () => {
      retry = rosterRetryFloor;
    };
    live.onmessage = (event) => {
      const payload = JSON.parse(event.data);
      if (payload.type === "agents") {
        onRoster(payload.agents ?? []);
      }
    };
    live.onclose = async () => {
      if (stopped || socket !== live) return;
      // A WebSocket close says nothing about why the handshake failed,
      // so ask over HTTP, where the status is legible.
      if (await tokenRejected()) {
        forgetToken();
        onLost("This token is no longer accepted — the server restarted. " +
          "Open the URL it printed.");
        return;
      }
      timer = window.setTimeout(connect, retry);
      retry = Math.min(retry * 2, rosterRetryCeiling);
    };
  };
  connect();

  return () => {
    stopped = true;
    window.clearTimeout(timer);
    socket?.close();
  };
}

/** tokenRejected reports whether the server is up and refusing us. */
async function tokenRejected(): Promise<boolean> {
  try {
    const response = await fetch(tokened("/api/agents"));
    return response.status === 401;
  } catch {
    // Unreachable is not refused: the server is down, and waiting is the
    // right answer.
    return false;
  }
}
