// Web Push endpoints. They always go to the Console's own server (the hub),
// never through a selected remote machine, and never carry a previewed
// tenant scope: a device subscribes as the person signed in to this Console.

export type PushKind = "waiting" | "tickets" | "machines";

export type PushSubscriptionRecord = {
  id: string;
  subscriber: string;
  kinds: PushKind[];
  label?: string;
  lang?: string;
  created_at: string;
  updated_at: string;
  last_success_at?: string;
  last_error_at?: string;
  last_error?: string;
};

export type PushStatus = {
  public_key: string;
  subscriber: string;
  kinds: PushKind[];
  subscriptions: PushSubscriptionRecord[];
};

export type PushDelivery = {
  subscription_id: string;
  label?: string;
  delivered: boolean;
  removed?: boolean;
  status?: number;
  error?: string;
};

export type PushSubscribeRequest = {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  kinds: PushKind[];
  label: string;
  lang: string;
};

const HEADERS = { "X-AgentMux-Console": "1" } as const;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    cache: "no-store",
    headers: body === undefined ? HEADERS : { ...HEADERS, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    const error = new Error(typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`) as Error & { status?: number };
    error.status = res.status;
    throw error;
  }
  return payload as T;
}

export const pushAPI = {
  status: () => call<PushStatus>("GET", "/api/v1/push"),
  subscribe: (request: PushSubscribeRequest) => call<PushSubscriptionRecord>("POST", "/api/v1/push/subscriptions", request),
  unsubscribe: (id: string) => call<{ ok: boolean }>("DELETE", `/api/v1/push/subscriptions?id=${encodeURIComponent(id)}`),
  test: (id?: string) => call<{ results: PushDelivery[] }>("POST", "/api/v1/push/test", id ? { id } : {}),
};
