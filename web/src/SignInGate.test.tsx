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

it("asks for sign-in, explains a failure, and opens the Console after success", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const authSession = vi.spyOn(api, "authSession").mockResolvedValue(signedOut);
  const signIn = vi.spyOn(api, "signIn")
    .mockRejectedValueOnce(new APIError("wrong login name or password", 401, {}))
    .mockResolvedValueOnce({ account: { id: "acct_1", login: "bob", role: "member", tenant_id: "ten_1" } });
  const { container, root } = await render(<SignInGate><p>console</p></SignInGate>);
  try {
    expect(container.textContent).not.toContain("console");
    const [login, password] = Array.from(container.querySelectorAll<HTMLInputElement>("input"));
    const form = container.querySelector("form")!;
    await act(async () => {
      setInput(login, "bob");
      setInput(password, "not-the-password");
    });
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(signIn).toHaveBeenLastCalledWith("bob", "not-the-password");
    expect(container.querySelector('[role="alert"]')?.textContent).toBe("Wrong login name or password.");

    authSession.mockResolvedValue({
      ...signedOut, sign_in_required: false, authenticated: true,
      account: { id: "acct_1", login: "bob", role: "member", tenant_id: "ten_1" }, tenant_id: "ten_1",
    });
    await act(async () => {
      setInput(password, "bob-password-1");
    });
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(signIn).toHaveBeenLastCalledWith("bob", "bob-password-1");
    expect(container.textContent).toContain("console");
  } finally {
    await act(async () => { root.unmount(); });
  }
});
