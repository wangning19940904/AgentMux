import { describe, expect, it, vi } from "vitest";
import swSource from "../../sw.js?raw";

type Listener = (event: any) => void;

// loadWorker evaluates web/sw.js the way the build ships it, against a small
// stand-in for the service worker global scope.
function loadWorker(precache: string[] = ["/", "/assets/index-abc.js", "/offline.html"]) {
  const source = swSource
    .replace('const BUILD_ID = "__BUILD_ID__";', 'const BUILD_ID = "test-build";')
    .replace("const PRECACHE = __PRECACHE__;", `const PRECACHE = ${JSON.stringify(precache)};`);
  const listeners = new Map<string, Listener>();
  const stores = new Map<string, Map<string, Response>>();
  const keyOf = (request: Request | string) => new URL(typeof request === "string" ? request : request.url, "https://hub.example").pathname;
  const caches = {
    open: vi.fn(async (name: string) => {
      if (!stores.has(name)) stores.set(name, new Map());
      const store = stores.get(name)!;
      return {
        addAll: vi.fn(async (requests: Request[]) => {
          for (const request of requests) store.set(keyOf(request), new Response(`cached ${keyOf(request)}`));
        }),
        match: vi.fn(async (request: Request | string) => store.get(keyOf(request))?.clone()),
        put: vi.fn(async (request: Request, response: Response) => {
          store.set(keyOf(request), response);
        }),
      };
    }),
    keys: vi.fn(async () => [...stores.keys()]),
    delete: vi.fn(async (name: string) => stores.delete(name)),
  };
  const windowClient = { url: "https://hub.example/#agents", focus: vi.fn(async () => undefined), postMessage: vi.fn() };
  const self: any = {
    location: { origin: "https://hub.example" },
    addEventListener: (type: string, listener: Listener) => listeners.set(type, listener),
    skipWaiting: vi.fn(),
    clients: { claim: vi.fn(async () => undefined), matchAll: vi.fn(async () => [windowClient]), openWindow: vi.fn() },
    registration: { showNotification: vi.fn(async () => undefined) },
  };
  const network = vi.fn(async (request: Request) => new Response(`network ${keyOf(request)}`, { status: 200 }));
  // Inside a worker, relative URLs resolve against its origin.
  const WorkerRequest = class extends Request {
    constructor(input: string, init?: RequestInit) {
      super(new URL(input, "https://hub.example"), init);
    }
  };
  new Function("self", "caches", "fetch", "Request", source)(self, caches, network, WorkerRequest);
  const dispatch = async (type: string, event: any) => {
    const pending: Promise<unknown>[] = [];
    let response: Promise<Response> | undefined;
    listeners.get(type)!({
      ...event,
      waitUntil: (promise: Promise<unknown>) => pending.push(promise),
      respondWith: (value: Promise<Response>) => {
        response = value;
      },
    });
    await Promise.all(pending);
    return response;
  };
  return { self, caches, stores, network, dispatch, windowClient };
}

const get = (path: string, mode: RequestMode | "navigate" = "cors") =>
  ({ url: `https://hub.example${path}`, method: "GET", mode });

describe("service worker", () => {
  it("never answers API, sign-in, or write requests", () => {
    const { self } = loadWorker();
    const classify = self.__agentmuxClassify as (url: URL, method: string, mode: string) => string;
    const at = (path: string) => new URL(path, "https://hub.example");
    expect(classify(at("/api/v1/agent-instances"), "GET", "cors")).toBe("network");
    expect(classify(at("/api/v1/push/subscriptions"), "POST", "cors")).toBe("network");
    expect(classify(at("/v1/responses"), "GET", "cors")).toBe("network");
    expect(classify(at("/console/enter"), "GET", "navigate")).toBe("network");
    expect(classify(at("/hook/abc"), "POST", "cors")).toBe("network");
    expect(classify(at("/sw.js"), "GET", "cors")).toBe("network");
    expect(classify(new URL("https://elsewhere.example/assets/x.js"), "GET", "cors")).toBe("network");
    expect(classify(at("/"), "GET", "navigate")).toBe("shell");
    expect(classify(at("/assets/index-abc.js"), "GET", "cors")).toBe("static");
    expect(classify(at("/icons/icon-192.png"), "GET", "no-cors")).toBe("static");
  });

  it("precaches one build and serves the shell from it", async () => {
    const { dispatch, network } = loadWorker();
    await dispatch("install", {});
    const shell = await dispatch("fetch", { request: get("/", "navigate") });
    expect(await shell!.text()).toBe("cached /");
    const asset = await dispatch("fetch", { request: get("/assets/index-abc.js") });
    expect(await asset!.text()).toBe("cached /assets/index-abc.js");
    expect(network).not.toHaveBeenCalled();
  });

  it("leaves API requests to the network untouched", async () => {
    const { dispatch } = loadWorker();
    await dispatch("install", {});
    expect(await dispatch("fetch", { request: get("/api/v1/status") })).toBeUndefined();
  });

  it("deletes older builds when it takes over, and only on request", async () => {
    const { dispatch, caches, self } = loadWorker();
    await caches.open("agentmux-shell-old-build");
    await caches.open("someone-elses-cache");
    await dispatch("install", {});
    expect(self.skipWaiting).not.toHaveBeenCalled();
    await dispatch("message", { data: { type: "SKIP_WAITING" } });
    expect(self.skipWaiting).toHaveBeenCalled();
    await dispatch("activate", {});
    expect(await caches.keys()).toEqual(["someone-elses-cache", "agentmux-shell-test-build"]);
    expect(self.clients.claim).toHaveBeenCalled();
  });

  it("shows pushed notifications and keeps taps inside the Console", async () => {
    const { dispatch, self, windowClient } = loadWorker();
    await dispatch("push", {
      data: { json: () => ({ title: "Approval needed", body: "Release bot is waiting", tag: "wait:a1", url: "https://evil.example/" }) },
    });
    const [title, options] = self.registration.showNotification.mock.calls[0];
    expect(title).toBe("Approval needed");
    expect(options).toMatchObject({ body: "Release bot is waiting", tag: "wait:a1", data: { url: "/" } });

    await dispatch("notificationclick", { notification: { close: vi.fn(), data: { url: "/#super-agents" } } });
    expect(windowClient.focus).toHaveBeenCalled();
    expect(windowClient.postMessage).toHaveBeenCalledWith({ type: "OPEN_URL", url: "/#super-agents" });
  });

  it("opens a window when no Console is open", async () => {
    const { dispatch, self } = loadWorker();
    self.clients.matchAll.mockResolvedValue([]);
    await dispatch("notificationclick", { notification: { close: vi.fn(), data: { url: "//evil.example" } } });
    expect(self.clients.openWindow).toHaveBeenCalledWith("/");
  });
});
