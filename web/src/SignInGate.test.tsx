// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api, APIError, type AuthSession } from "./api";
import { I18nProvider } from "./i18n";
import { SignInGate } from "./SignInGate";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

const signedOut: AuthSession = {
  accounts_enabled: true,
  setup_available: false,
  sign_in_required: true,
  authenticated: false,
  admin: false,
  sign_in_methods: ["password"],
};

function setInput(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

async function render(node: React.ReactNode) {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(<I18nProvider language="en">{node}</I18nProvider>);
  });
  return { container, root };
}

it("asks for the first administrator before a machine without accounts can be used", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const noAccounts = { ...signedOut, accounts_enabled: false, setup_available: true, sign_in_required: false, authenticated: true, admin: true };
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(noAccounts);
  vi.spyOn(api, "identify").mockResolvedValue({ login: "ning", state: "unregistered", setup_required: true, registration_open: false });
  const setup = vi.spyOn(api, "setupFirstAdmin").mockResolvedValue({
    account: { id: "acct_1", login: "ning", role: "admin", status: "active", has_password: true, created_at: "", updated_at: "" },
  });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).not.toContain("console");
    expect(container.textContent).toContain("Create the first administrator");
    const [login, name, password, confirm] = visibleInputs(container);
    await act(async () => {
      setInput(login, "ning");
      setInput(name, "Ning");
      setInput(password, "ning-password-1");
      setInput(confirm, "ning-password-1");
    });
    authSession.mockResolvedValue({ ...noAccounts, accounts_enabled: true, account: { id: "acct_1", login: "ning", role: "admin" } });
    await act(async () => { submit(container); });
    expect(setup).toHaveBeenCalledWith({ login: "ning", display_name: "Ning", password: "ning-password-1" });
    expect(container.textContent).toContain("console");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

function submit(container: HTMLElement) {
  const form = container.querySelector("form")!;
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
}

function visibleInputs(container: HTMLElement) {
  return Array.from(container.querySelectorAll<HTMLInputElement>("input")).filter((input) => !input.hidden);
}

it("asks for the name first, then the password, explains a failure, and opens the Console after success", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  const identify = vi.spyOn(api, "identify").mockResolvedValue({
    login: "bob", state: "registered", setup_required: false, registration_open: true,
  });
  const signIn = vi.spyOn(api, "signIn")
    .mockRejectedValueOnce(new APIError("wrong login name or password", 401, {}))
    .mockResolvedValueOnce({ account: { id: "acct_1", login: "bob", role: "member" } });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).not.toContain("console");
    expect(visibleInputs(container)).toHaveLength(1);
    await act(async () => { setInput(visibleInputs(container)[0], " Bob "); });
    await act(async () => { submit(container); });
    expect(identify).toHaveBeenLastCalledWith("bob");
    expect(container.textContent).toContain("You have an account");

    const [password] = visibleInputs(container);
    expect(password.type).toBe("password");
    await act(async () => { setInput(password, "not-the-password"); });
    await act(async () => { submit(container); });
    expect(signIn).toHaveBeenLastCalledWith("bob", "not-the-password");
    expect(container.querySelector('[role="alert"]')?.textContent).toBe("Wrong login name or password.");

    authSession.mockResolvedValue({
      ...signedOut, sign_in_required: false, authenticated: true,
      account: { id: "acct_1", login: "bob", role: "member" },
    });
    await act(async () => { setInput(visibleInputs(container)[0], "bob-password-1"); });
    await act(async () => { submit(container); });
    expect(signIn).toHaveBeenLastCalledWith("bob", "bob-password-1");
    expect(container.textContent).toContain("console");
    // The browser remembers who signed in, by name only.
    const stored = window.localStorage.getItem("agentmux.signIn.savedAccounts") ?? "";
    expect(JSON.parse(stored)).toMatchObject([{ login: "bob" }]);
    expect(stored).not.toContain("password");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("guides an unknown name through registration to waiting for approval", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  vi.spyOn(api, "identify").mockResolvedValue({
    login: "carol", state: "unregistered", setup_required: false, registration_open: true,
  });
  const register = vi.spyOn(api, "register").mockResolvedValue({ account: { id: "acct_2", login: "carol", role: "member" }, status: "pending" });
  const signIn = vi.spyOn(api, "signIn").mockRejectedValue(new APIError("waiting", 403, { code: "pending" }));
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    await act(async () => { setInput(visibleInputs(container)[0], "carol"); });
    await act(async () => { submit(container); });
    expect(container.textContent).toContain("Create your account");
    expect(container.textContent).toContain("There is no account named carol yet");

    const [name, password, confirm] = visibleInputs(container);
    await act(async () => {
      setInput(name, "Carol");
      setInput(password, "carol-password-1");
      setInput(confirm, "carol-password-2");
    });
    await act(async () => { submit(container); });
    expect(register).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')?.textContent).toBe("The two passwords differ.");

    await act(async () => { setInput(visibleInputs(container)[2], "carol-password-1"); });
    await act(async () => { submit(container); });
    expect(register).toHaveBeenCalledWith({ login: "carol", display_name: "Carol", password: "carol-password-1" });
    expect(container.textContent).toContain("Waiting for approval");

    // Trying to sign in before approval comes back to the same place.
    const retry = Array.from(container.querySelectorAll("button")).find((button) => button.textContent?.includes("Sign in"))!;
    await act(async () => { retry.click(); });
    await act(async () => { setInput(visibleInputs(container)[0], "carol-password-1"); });
    await act(async () => { submit(container); });
    expect(signIn).toHaveBeenCalledWith("carol", "carol-password-1");
    expect(container.textContent).toContain("Waiting for approval");
    expect(container.textContent).not.toContain("console");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("asks for the password straight away when the daemon cannot identify names", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  vi.spyOn(api, "identify").mockRejectedValue(new APIError("404 page not found", 404, {}));
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    await act(async () => { setInput(visibleInputs(container)[0], "dave"); });
    await act(async () => { submit(container); });
    const [password] = visibleInputs(container);
    expect(password.type).toBe("password");
    expect(container.textContent).toContain("dave");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("keeps the desktop app on sign-in until its machine has an account, creating the first one when none exists", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("location", { ...window.location, protocol: "wails:" });
  const desktopSession: AuthSession = {
    ...signedOut, accounts_enabled: false, sign_in_required: false, authenticated: true, admin: true,
    device: { signed_in: false, authority: "primary", setup_allowed: true },
  };
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(desktopSession);
  const identify = vi.spyOn(api, "deviceIdentify").mockResolvedValue({
    login: "ning", state: "unregistered", setup_required: true, registration_open: false,
  });
  const browserIdentify = vi.spyOn(api, "identify");
  const setup = vi.spyOn(api, "deviceSetup").mockResolvedValue({ account: { id: "acct_1", login: "ning", role: "admin" } });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).not.toContain("console");
    expect(container.textContent).toContain("Sign in on this machine");
    await act(async () => { setInput(visibleInputs(container)[0], "ning"); });
    await act(async () => { submit(container); });
    expect(identify).toHaveBeenCalledWith("ning");
    expect(browserIdentify).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Create the first administrator");

    const [name, password, confirm] = visibleInputs(container);
    await act(async () => {
      setInput(name, "Ning");
      setInput(password, "ning-password-1");
      setInput(confirm, "ning-password-1");
    });
    authSession.mockResolvedValue({
      ...desktopSession, accounts_enabled: true,
      account: { id: "acct_1", login: "ning", display_name: "Ning", role: "admin" },
      device: { signed_in: true, account: { id: "acct_1", login: "ning", role: "admin" }, authority: "primary", setup_allowed: true },
    });
    await act(async () => { submit(container); });
    expect(setup).toHaveBeenCalledWith({ login: "ning", display_name: "Ning", password: "ning-password-1" });
    expect(container.textContent).toContain("console");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

function button(container: HTMLElement, text: string) {
  return Array.from(container.querySelectorAll("button")).find((candidate) => candidate.textContent?.includes(text))!;
}

it("offers the accounts saved on this machine and signs in to one with just its password", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("location", { ...window.location, protocol: "wails:" });
  const desktopSession: AuthSession = {
    ...signedOut, accounts_enabled: true, sign_in_required: false, authenticated: true, admin: true,
    device: {
      signed_in: false, authority: "primary", setup_allowed: true,
      saved_accounts: [
        { login: "ning", display_name: "Ning Wang", last_signed_in_at: new Date(Date.now() - 3 * 3_600_000).toISOString() },
        { login: "bob" },
      ],
    },
  };
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(desktopSession);
  const identify = vi.spyOn(api, "deviceIdentify");
  const signIn = vi.spyOn(api, "deviceSignIn").mockResolvedValue({ account: { id: "acct_1", login: "ning", role: "admin" } });
  const forget = vi.spyOn(api, "deviceForget").mockResolvedValue({ ok: true });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).toContain("Choose an account");
    expect(container.querySelector('[role="note"]')?.textContent).toContain("Saved on this machine only");
    expect(container.textContent).toContain("Ning Wang");
    expect(container.textContent).toContain("last signed in 3 h ago");
    expect(visibleInputs(container)).toHaveLength(0);

    // Removing one keeps it off this machine's list.
    await act(async () => { container.querySelector<HTMLButtonElement>('button[aria-label="Remove bob from this list"]')!.click(); });
    expect(forget).toHaveBeenCalledWith("bob");
    expect(container.textContent).not.toContain("bob");

    await act(async () => { button(container, "Ning Wang").click(); });
    expect(identify).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Welcome back");
    const [password] = visibleInputs(container);
    expect(password.type).toBe("password");
    authSession.mockResolvedValue({ ...desktopSession, device: { ...desktopSession.device!, signed_in: true } });
    await act(async () => { setInput(password, "ning-password-1"); });
    await act(async () => { submit(container); });
    expect(signIn).toHaveBeenCalledWith("ning", "ning-password-1");
    expect(container.textContent).toContain("console");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("creates an account from the first screen, asking for approval when accounts already exist", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  const identify = vi.spyOn(api, "identify").mockResolvedValue({
    login: "erin", state: "unregistered", setup_required: false, registration_open: true,
  });
  const register = vi.spyOn(api, "register").mockResolvedValue({ account: { id: "acct_5", login: "erin", role: "member" }, status: "pending" });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    await act(async () => { button(container, "Create account").click(); });
    expect(container.textContent).toContain("Create your account");
    const [login, name, password, confirm] = visibleInputs(container);
    await act(async () => {
      setInput(login, "Erin");
      setInput(name, "Erin");
      setInput(password, "erin-password-1");
      setInput(confirm, "erin-password-1");
    });
    await act(async () => { submit(container); });
    expect(identify).toHaveBeenCalledWith("erin");
    expect(register).toHaveBeenCalledWith({ login: "erin", display_name: "Erin", password: "erin-password-1" });
    expect(container.textContent).toContain("Waiting for approval");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

it("sends a new account that is already registered to its password instead", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  vi.spyOn(api, "identify").mockResolvedValue({
    login: "bob", state: "registered", setup_required: false, registration_open: true,
  });
  const register = vi.spyOn(api, "register");
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    await act(async () => { button(container, "Create account").click(); });
    const [login, , password, confirm] = visibleInputs(container);
    await act(async () => {
      setInput(login, "bob");
      setInput(password, "bob-password-1");
      setInput(confirm, "bob-password-1");
    });
    await act(async () => { submit(container); });
    expect(register).not.toHaveBeenCalled();
    expect(container.querySelector('[role="status"]')?.textContent).toContain("already registered");
    expect(visibleInputs(container)[0].type).toBe("password");
  } finally {
    await act(async () => { root.unmount(); });
  }
});

const nodeSession: AuthSession = {
  ...signedOut, accounts_enabled: false, sign_in_required: false, authenticated: true, admin: true,
  device: {
    signed_in: false, authority: "hub", setup_allowed: false,
    hub_accounts: [{ account: { id: "acct_1", login: "admin", display_name: "Admin", role: "admin" }, machine: "J7FGQK0CR2" }],
  },
};

it("signs a node in with its hub's account once the hub approves", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(nodeSession);
  const request = vi.spyOn(api, "deviceRequestApproval").mockResolvedValue({
    approval: { id: "ap_1", login: "admin", machine: "ecs_cn", state: "pending", created_at: "", expires_at: "" },
  });
  const status = vi.spyOn(api, "deviceApprovalStatus")
    .mockResolvedValueOnce({ approval: { id: "ap_1", login: "admin", machine: "ecs_cn", state: "pending", created_at: "", expires_at: "" } })
    .mockResolvedValueOnce({ account: { id: "acct_1", login: "admin", role: "admin" } });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).toContain("This machine is connected to a hub");
    expect(container.textContent).toContain("admin · signed in on J7FGQK0CR2");
    await act(async () => { button(container, "Ask the hub to approve").click(); });
    expect(request).toHaveBeenCalledWith("admin");
    expect(container.textContent).toContain("Waiting for approval");
    await act(async () => { vi.advanceTimersByTime(2000); });
    expect(container.textContent).not.toContain("console");
    authSession.mockResolvedValue({ ...nodeSession, device: { ...nodeSession.device!, signed_in: true } });
    await act(async () => { vi.advanceTimersByTime(2000); });
    expect(status).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("console");
  } finally {
    vi.useRealTimers();
    await act(async () => { root.unmount(); });
  }
});

it("leaves the hub only after the person confirms, and explains a taken name", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue(nodeSession);
  const leave = vi.spyOn(api, "deviceLeaveHub").mockRejectedValue(new APIError("taken", 409, { code: "registered" }));
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    await act(async () => { button(container, "Use another account").click(); });
    expect(container.textContent).toContain("This machine will disconnect from the hub");
    const inputs = visibleInputs(container);
    const [login, , password, confirm] = inputs;
    await act(async () => {
      setInput(login, "admin");
      setInput(password, "solo-password-1");
      setInput(confirm, "solo-password-1");
    });
    const submitButton = button(container, "Disconnect and create account");
    expect(submitButton.disabled).toBe(true);
    await act(async () => { inputs.find((input) => input.type === "checkbox")!.click(); });
    expect(submitButton.disabled).toBe(false);
    await act(async () => { submit(container); });
    expect(leave).toHaveBeenCalledWith({ login: "admin", display_name: undefined, password: "solo-password-1" });
    expect(container.querySelector('[role="alert"]')?.textContent).toContain("already has an account with this login name");
  } finally {
    await act(async () => { root.unmount(); });
  }
});
