// The domain, as the API serves it. These mirror internal/agent and
// internal/workspace; the names are the JSON tags, not the Go fields.

export type Activity =
  | "starting"
  | "working"
  | "idle"
  | "completed"
  | "failed"
  | "stopped";

/** What an agent needs from a human. "" is nothing. */
export type Attention = "" | "question" | "approval" | "auth" | "waiting";

/** The human's own reading of an agent, overriding what was inferred. */
export type Mark = "" | "working" | "attention";

export interface Workspace {
  id: string;
  kind: string;
  name: string;
  root: string;
  execution_root: string;
  /** The machine this workspace is on; absent means this one. */
  host?: string;
  component_name?: string;
  component_root?: string;
}

/**
 * A link: an arrow from one agent to another that sends. When the
 * source's turn ends (auto) or when fired by hand, the target receives
 * the label and the source's last reply. The server owns links — they
 * fire from the provider hook whether or not this page is open — and
 * pushes them with every roster.
 */
export interface Link {
  id: string;
  from: string;
  to: string;
  label: string;
  auto: boolean;
  last_fired?: string;
  pending?: { message: string; hop: number; at: string };
}

export interface Agent {
  id: string;
  provider: string;
  name: string;
  task: string;
  summary?: string;
  cwd: string;
  created_at: string;
  activity: Activity;
  attention?: Attention;
  attention_at?: string;
  mark?: Mark;
  process_live: boolean;
  exit_code?: number;
  mode?: string;
  session_id?: string;
  workspace: Workspace;
  /** The machine that answered for this agent; absent means this one. */
  host?: string;
}

/** One conversation event, as /api/agents/{id}/transcript serves it. */
export interface TranscriptEntry {
  kind: "prompt" | "reply" | "tool" | "result";
  text?: string;
  tool?: string;
  arg?: string;
  /** Result lines elided server-side. */
  hidden?: number;
}

export interface Provider {
  ID: string;
  Label: string;
  Available: boolean;
  Path: string;
}

/** Urgent attention blocks the agent on a decision; waiting does not. */
export function isUrgent(agent: Agent): boolean {
  return agent.attention === "question"
    || agent.attention === "approval"
    || agent.attention === "auth";
}

/** The mark a dashboard honors: a dead process has its own story. */
export function effectiveMark(agent: Agent): Mark {
  return agent.process_live ? (agent.mark ?? "") : "";
}
