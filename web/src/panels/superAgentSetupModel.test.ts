import { describe, expect, it } from "vitest";
import type { SuperAgentSource } from "../api";
import { newSource } from "./superAgentModel";
import {
  emailProviderForAddress, emailProviderForConfig, githubForwardCommand, githubHookSettingsURL, githubPublicBase,
  githubTokenURL, githubWebBase, isReachableOrigin, isRepoName, tokenCanComment,
} from "./superAgentSetupModel";

const github = (config: Record<string, string>): SuperAgentSource => ({ id: "src_1", kind: "github_webhook", enabled: true, allowed_actors: [], config });

describe("trigger setup model", () => {
  it("knows which origins GitHub cannot deliver to", () => {
    for (const origin of ["wails://wails.localhost", "http://localhost:8765", "http://127.0.0.1:8765", "http://[::1]:8765", "http://10.1.2.3", "http://192.168.1.4:8765", "http://172.20.0.1", "http://100.100.1.1", "http://mac.local", "https://[fd00::1]", "not a url"]) {
      expect(isReachableOrigin(origin)).toBe(false);
    }
    for (const origin of ["https://agentmux.example.com", "http://203.0.113.9:8765", "https://fcbarcelona.example", "https://[2001:db8::1]"]) {
      expect(isReachableOrigin(origin)).toBe(true);
    }
  });

  it("prefers the configured public address over the page's origin", () => {
    expect(githubPublicBase(github({ public_url: "https://hooks.example.com/ " }), "http://127.0.0.1:8765")).toBe("https://hooks.example.com");
    expect(githubPublicBase(github({}), "https://agentmux.example.com")).toBe("https://agentmux.example.com");
    expect(githubPublicBase(github({}), "wails://wails.localhost")).toBe("");
  });

  it("writes a forward command for this repository and secret", () => {
    expect(githubForwardCommand(github({ repo: "acme/app", webhook_secret: "abc" }), "http://127.0.0.1:8765/")).toBe(
      "gh webhook forward --repo=acme/app --events=issues,issue_comment,pull_request,pull_request_review_comment --url=http://127.0.0.1:8765/hook/super-agent/github/src_1 --secret=abc",
    );
    expect(githubForwardCommand(github({ repo: "acme/app", webhook_secret: "abc" }), "http://127.0.0.1:8765", true)).toMatch(/--secret=•{8}$/);
    // A saved secret is not readable, and GitHub Enterprise needs its host.
    const enterprise = githubForwardCommand(github({ repo: "acme/app", webhook_secret: "<redacted>", api_base: "https://git.acme.com/api/v3" }), "http://127.0.0.1:8765");
    expect(enterprise).toContain("--secret=YOUR_WEBHOOK_SECRET");
    expect(enterprise).toMatch(/--github-host=git\.acme\.com$/);
  });

  it("links to the right GitHub pages", () => {
    expect(githubWebBase("")).toBe("https://github.com");
    expect(githubWebBase("https://git.acme.com/api/v3/")).toBe("https://git.acme.com");
    expect(githubHookSettingsURL("acme/app", "")).toBe("https://github.com/acme/app/settings/hooks/new");
    expect(githubTokenURL("https://git.acme.com/api/v3")).toBe("https://git.acme.com/settings/tokens/new?scopes=repo&description=AgentMux+Super+Agent");
    expect(isRepoName("acme/app.js")).toBe(true);
    expect(isRepoName("acme")).toBe(false);
    expect(tokenCanComment(["public_repo"])).toBe(true);
    expect(tokenCanComment(["read:org"])).toBe(false);
  });

  it("recognises common mail providers", () => {
    expect(emailProviderForAddress("Agent@QQ.com")?.imapHost).toBe("imap.qq.com");
    expect(emailProviderForAddress("agent@icloud.com")?.smtpPort).toBe("587");
    expect(emailProviderForAddress("agent@acme.com")).toBeUndefined();
    expect(emailProviderForConfig({ imap_host: "imap.feishu.cn" })?.id).toBe("feishu");
  });

  it("gives a new GitHub trigger its id and secret before it is saved", () => {
    const source = newSource("github_webhook");
    expect(source.id).toMatch(/^src_/);
    expect(source.config.webhook_secret).toMatch(/^[0-9a-f]{64}$/);
    expect(newSource("github_webhook").config.webhook_secret).not.toBe(source.config.webhook_secret);
    // Polling unless GitHub can reach the page.
    expect(source.config.delivery).toBe("poll");
    expect(newSource("github_webhook", true).config.delivery).toBe("webhook");
    expect(newSource("email").config).toEqual({});
  });
});
