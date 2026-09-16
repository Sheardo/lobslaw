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
  is_chief: boolean;
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
};

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
