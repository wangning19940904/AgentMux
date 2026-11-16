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

it("opens the Console directly on a machine without accounts", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "authSession").mockResolvedValue({ ...signedOut, accounts_enabled: false, sign_in_required: false, authenticated: true, admin: true });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).toContain("console");
    expect(container.querySelector("form")).toBeNull();
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
