// The console's whole view of the node.
//
// Hand-written rather than generated: the surface is ten calls, and a
// generator would be a build-step dependency plus a schema to keep in
// step, for types a person can read in one screen.

export type BotStatus = "pending" | "claimed" | "done" | "failed" | "cancelled";
export type InboxKind = "task" | "question" | "answer" | "result" | "fyi";

export interface Bot {
  id: string;
  display_name: string;
  description: string;
  instructions: string;
  is_coordinator: boolean;
  enabled: boolean;
  tools: string[];
  may_message: string[];
  revision: number;
  created_at?: string;
  updated_at?: string;
}

export interface InboxItem {
  id: string;
  recipient: string;
  sender: string;
  kind: InboxKind;
  subject: string;
  body?: string;
  priority: number;
  status: BotStatus;
  result?: string;
  error?: string;
  attempts: number;
  correlation_id?: string;
  session_id?: string;
  created_at?: string;
  completed_at?: string;
}

/** ApiError carries the status so a caller can tell a conflict from a
 * typo — 409 means somebody else edited this while your form was open,
 * and telling the user to "try again" is only useful if you know that. */
export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  if (!res.ok) {
    // The API's error body is {"error": "..."}. Falling back to the raw
    // text matters for the cases that do not reach the handler at all —
    // a reverse proxy's 502, say — where a JSON parse failure would
    // replace a useful message with "unexpected token".
    let message = text;
    try {
      message = (JSON.parse(text) as { error?: string }).error ?? text;
    } catch {
      /* keep the raw body */
    }
    throw new ApiError(res.status, message || res.statusText);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export const api = {
  listBots: () => request<{ bots: Bot[] }>("/v1/bots").then((r) => r.bots),

  getBot: (id: string) => request<Bot>(`/v1/bots/${encodeURIComponent(id)}`),

  createBot: (bot: Partial<Bot>) =>
    request<Bot>("/v1/bots", { method: "POST", body: JSON.stringify(bot) }),

  updateBot: (id: string, patch: Partial<Bot>) =>
    request<Bot>(`/v1/bots/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    }),

  deleteBot: (id: string) =>
    request<void>(`/v1/bots/${encodeURIComponent(id)}`, { method: "DELETE" }),

  listInbox: (botId: string, status?: string) => {
    const q = status && status !== "all" ? `?status=${status}` : "?status=all";
    return request<{ items: InboxItem[] }>(
      `/v1/bots/${encodeURIComponent(botId)}/inbox${q}`,
    ).then((r) => r.items ?? []);
  },

  assign: (botId: string, item: { subject: string; body: string; kind: InboxKind; priority: number }) =>
    request<InboxItem>(`/v1/bots/${encodeURIComponent(botId)}/inbox`, {
      method: "POST",
      body: JSON.stringify(item),
    }),

  readItem: (botId: string, itemId: string) =>
    request<InboxItem>(
      `/v1/inbox/${encodeURIComponent(botId)}/${encodeURIComponent(itemId)}`,
    ),

  actOnItem: (botId: string, itemId: string, action: "retry" | "cancel") =>
    request<InboxItem>(
      `/v1/inbox/${encodeURIComponent(botId)}/${encodeURIComponent(itemId)}`,
      { method: "PATCH", body: JSON.stringify({ action }) },
    ),

  activity: (limit = 100) =>
    request<{ items: InboxItem[] }>(`/v1/activity?limit=${limit}`).then(
      (r) => r.items ?? [],
    ),

  sendMessage: (text: string) =>
    request<{ reply?: string; needs_confirmation?: boolean }>("/v1/messages", {
      method: "POST",
      body: JSON.stringify({ message: text }),
    }),

  whoami: () => request<Whoami>("/v1/auth/whoami"),

  login: (token: string) =>
    request<{ scope: string; expires_at: string }>("/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ token }),
    }),

  logout: () => request<void>("/v1/auth/login", { method: "DELETE" }),

  config: () => request<NodeConfig>("/v1/config"),

  botSessions: (botId: string) =>
    request<{ sessions: Session[] }>(
      `/v1/bots/${encodeURIComponent(botId)}/sessions`,
    ).then((r) => r.sessions ?? []),

  transcript: (sessionId: string) =>
    request<{ messages: TranscriptMessage[] }>(
      `/v1/sessions/${encodeURIComponent(sessionId)}`,
    ).then((r) => r.messages ?? []),
};

export interface Whoami {
  authenticated: boolean;
  subject?: string;
  scope?: string;
  login_available: boolean;
  reason?: string;
}

export interface Session {
  id: string;
  channel: string;
  channel_id: string;
  title?: string;
  messages: number;
  updated_at?: string;
}

export interface TranscriptMessage {
  seq: number;
  role: string;
  content: string;
  tool_calls?: number;
  turn_id?: string;
}

export interface NodeConfig {
  node_id: string;
  version?: string;
  functions: string[];
  gateway: {
    enabled: boolean;
    bind_address: string;
    http_port: number;
    require_auth: boolean;
    ui_enabled: boolean;
    login_configured: boolean;
    default_timezone?: string;
    queue_mode?: string;
  };
  compute: {
    providers: { label: string; trust_tier?: string; roles?: string[] }[];
    max_tool_calls_per_turn: number;
    self_learning_mode?: string;
  };
  memory: { enabled: boolean; dream_schedule?: string; embedding_model?: string };
  bots: { max_pending: number; drain_enabled: boolean };
  channels: { type: string; enabled: boolean }[];
}

/** streamBotChat talks to ONE bot over SSE.
 *
 * Hand-parsed rather than using EventSource, because EventSource
 * cannot issue a POST — and the message has to go in a body, not a
 * query string, where it would end up in every access log between here
 * and the node. */
export async function streamBotChat(
  botId: string,
  message: string,
  onEvent: (event: string, data: Record<string, unknown>) => void,
): Promise<void> {
  const res = await fetch(`/v1/bots/${encodeURIComponent(botId)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
  });
  if (!res.ok || !res.body) {
    const text = await res.text();
    let msg = text;
    try {
      msg = (JSON.parse(text) as { error?: string }).error ?? text;
    } catch {
      /* keep the raw body */
    }
    throw new ApiError(res.status, msg || res.statusText);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    // Events are separated by a blank line. Anything after the last
    // one is a partial frame and stays in the buffer.
    const frames = buffer.split("\n\n");
    buffer = frames.pop() ?? "";
    for (const frame of frames) {
      let event = "message";
      let data = "{}";
      for (const line of frame.split("\n")) {
        if (line.startsWith("event: ")) event = line.slice(7).trim();
        if (line.startsWith("data: ")) data = line.slice(6);
      }
      try {
        onEvent(event, JSON.parse(data) as Record<string, unknown>);
      } catch {
        /* a frame we cannot parse is one we cannot act on */
      }
    }
  }
}

/** Colours for a queue item's status.
 *
 * One mapping, used by every view, so an item is the same colour in
 * the activity feed as it is on the bot's own page — a reader
 * scanning for red should not have to check which screen they are on.
 */
export const statusPalette: Record<BotStatus, string> = {
  pending: "gray",
  claimed: "blue",
  done: "green",
  failed: "red",
  cancelled: "orange",
};
