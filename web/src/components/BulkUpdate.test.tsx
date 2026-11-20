import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "../i18n";
import { BulkUpdateButton, BulkUpdateResults } from "./BulkUpdate";

describe("bulk update controls", () => {
  it("shows the update count, disables empty/busy actions, and supports both languages", () => {
    const renderButton = (count: number, disabled = false, running = false) => renderToStaticMarkup(
      <I18nProvider language="zh"><BulkUpdateButton count={count} disabled={disabled}
        progress={running ? { completed: 1, total: 3 } : null} hint="scope" onClick={() => {}} /></I18nProvider>,
    );
    expect(renderButton(3)).toContain("更新全部");
    expect(renderButton(3)).toContain(">3</span>");
    expect(renderButton(3)).not.toContain("disabled");
    expect(renderButton(0)).toContain("disabled");
    expect(renderButton(3, true)).toContain("disabled");
    expect(renderButton(3, false, true)).toContain("更新中...");
    expect(renderButton(3, false, true)).toContain("disabled");
    const english = renderToStaticMarkup(<I18nProvider language="en"><BulkUpdateButton count={1} disabled={false} progress={null} hint="scope" onClick={() => {}} /></I18nProvider>);
    expect(english).toContain("Update all");
  });

  it("renders progress and a partial-failure summary with machine names and logs", () => {
    const results = [
      { key: "a", label: "Codex · local", result: { ok: true } },
      { key: "b", label: "Claude Code · ssh-1", result: { ok: false, error: "offline", log: "connection log" } },
    ];
    const render = (running: boolean) => renderToStaticMarkup(<I18nProvider language="zh">
      <BulkUpdateResults results={results} progress={running ? { completed: 2, total: 3 } : null} />
    </I18nProvider>);
    expect(render(true)).toContain("已完成 2/3");
    const complete = render(false);
    expect(complete).toContain("成功 1 项，失败 1 项");
    expect(complete).toContain("Claude Code · ssh-1");
    expect(complete).toContain("offline");
    expect(complete).toContain("connection log");
    expect(complete).toContain('role="status"');
    expect(complete).toContain('<details open="">');
  });

  it("shows the running item's phase, output and log, the queue, and warnings", () => {
    const results = [
      { key: "a", label: "Codex · local", result: { ok: true, warning: "brew exited 1, but Codex is now 0.169.0" } },
      { key: "b", label: "Claude Code · home", running: true, progress: { phase: "updating", detail: "==> Upgrading", percent: 30, log: "==> Fetching\n==> Upgrading\n" } },
      { key: "c", label: "Cursor · ecs" },
    ];
    const html = renderToStaticMarkup(<I18nProvider language="zh">
      <BulkUpdateResults results={results} progress={{ completed: 1, total: 3 }} />
    </I18nProvider>);
    expect(html).toContain("已完成 1/3");
    expect(html).toContain("当前：Claude Code · home");
    expect(html).toContain("==&gt; Upgrading");
    expect(html).toContain("命令输出");
    expect(html).toContain("==&gt; Fetching");
    expect(html).toContain("排队中（1）");
    expect(html).toContain("Cursor · ecs");
    expect(html).toContain("已更新（有警告）");
    expect(html).toContain("brew exited 1, but Codex is now 0.169.0");
  });
});
