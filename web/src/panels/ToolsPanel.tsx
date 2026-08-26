import { Bot, CheckCircle2, Download, ExternalLink, LogIn, RefreshCw, Search, ShieldCheck, TerminalSquare, TriangleAlert, X } from "lucide-react";
import { useEffect, useState } from "react";
import {
  api,
  BundleInstallResult,
  CLIAuthSession,
  CLIAuthStatus,
  CLIInstallResult,
  CLIManagedTool,
  CLIUpdateCheck,
  MarketplaceSkill,
  OperationProgress,
  Skill,
  ToolBundle,
} from "../api";
import { isDesktopApp, openExternalURL } from "../api/desktop";
import { CATALOG_PAGE_SIZE, CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { OperationProgress as OperationProgressView } from "../components/OperationProgress";
import { InternalOnlyDialog } from "../components/InternalOnlyDialog";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

type CLIBusyAction = "install" | "update" | "check" | "sync" | "auth";
type InternalInstallTarget =
  | { kind: "cli"; id: string; name: string; action: "install" }
  | { kind: "bundle"; id: string; name: string; components: string[] };

export function ToolsPanel() {
  const { t } = useI18n();
  const tools = useAsync(() => api.tools(), []);
  const [marketQuery, setMarketQuery] = useState("");
  const marketplace = useAsync(() => api.skillMarketplace(marketQuery), [marketQuery]);
  const [busy, setBusy] = useState("");
  const [cliBusy, setCliBusy] = useState<Record<string, CLIBusyAction>>({});
  const [cliProgress, setCliProgress] = useState<Record<string, OperationProgress>>({});
  const [cliChecks, setCliChecks] = useState<Record<string, CLIUpdateCheck>>({});
  const [cliAuth, setCLIAuth] = useState<Record<string, CLIAuthStatus>>({});
  const [cliAuthSessions, setCLIAuthSessions] = useState<Record<string, CLIAuthSession>>({});
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<CLIInstallResult | null>(null);
  const [bundleBusy, setBundleBusy] = useState("");
  const [bundleProgress, setBundleProgress] = useState<Record<string, OperationProgress>>({});
  const [bundleResult, setBundleResult] = useState<BundleInstallResult | null>(null);
  const [internalTarget, setInternalTarget] = useState<InternalInstallTarget | null>(null);

  const data = tools.data;
  const cli = data?.cli ?? [];
  const bundles = data?.bundles ?? [];
  const skills = data?.skills ?? [];
  const market = marketplace.data ?? data?.marketplace ?? [];
  const installTarget = cliInstallTarget();
  const authTargetKey = cli
    .filter((item) => item.installed && item.spec.login_supported)
    .map((item) => item.spec.id)
    .sort()
    .join(",");
  const activeAuthSessionKey = Object.values(cliAuthSessions)
    .filter((session) => !cliAuthSessionTerminal(session.state))
    .map((session) => session.session_id)
    .sort()
    .join(",");
  const sortedCLI = [...cli].sort((left, right) => Number(right.installed) - Number(left.installed));
  const cliPagination = useCatalogPagination(sortedCLI);
  const marketPagination = useCatalogPagination(market, marketQuery);
  const skillPagination = useCatalogPagination(skills);

  useEffect(() => {
    cli.forEach((item) => {
      const id = item.spec.id;
      if (!item.installed || cliChecks[id] || cliBusy[id]) return;
      void checkCLIUpdate(id, true);
    });
  }, [cli, cliBusy, cliChecks]);

  useEffect(() => {
    const targetIndex = sortedCLI.findIndex((item) => item.spec.id === installTarget);
    if (targetIndex < 0) return;
    const targetPage = Math.floor(targetIndex / CATALOG_PAGE_SIZE) + 1;
    if (cliPagination.page !== targetPage) {
      cliPagination.setPage(targetPage);
      return;
    }
    const frame = window.requestAnimationFrame(() => {
      document.getElementById(`cli-tool-${installTarget}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [cli, installTarget, cliPagination.page]);

  useEffect(() => {
    if (!authTargetKey) return;
    let active = true;
    void Promise.all(authTargetKey.split(",").map(async (id) => {
      try {
        return await api.cliAuth(id);
      } catch {
        return null;
      }
    })).then((statuses) => {
      if (!active) return;
      setCLIAuth((current) => {
        const next = { ...current };
        statuses.forEach((status) => {
          if (status) next[status.id] = status;
        });
        return next;
      });
    });
    return () => { active = false; };
  }, [authTargetKey]);

  useEffect(() => {
    if (!activeAuthSessionKey) return;
    let active = true;
    const sessionIDs = activeAuthSessionKey.split(",");
    const poll = async () => {
      await Promise.all(sessionIDs.map(async (sessionID) => {
        try {
          const snapshot = await api.cliAuthSession(sessionID);
          if (!active) return;
          setCLIAuthSessions((current) => ({ ...current, [snapshot.id]: snapshot }));
          if (cliAuthSessionTerminal(snapshot.state)) {
            const status = await api.cliAuth(snapshot.id);
            if (!active) return;
            setCLIAuth((current) => ({ ...current, [snapshot.id]: status }));
            if (snapshot.state === "succeeded") setNotice(t("tools.authReady"));
            else if (snapshot.error) setNotice(snapshot.error);
          }
        } catch (err) {
          if (active) setNotice(err instanceof Error ? err.message : String(err));
        }
      }));
    };
    void poll();
    const timer = window.setInterval(() => { void poll(); }, 1500);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [activeAuthSessionKey]);

  async function refreshAll() {
    setCliChecks({});
    await Promise.all([tools.reload(), marketplace.reload()]);
  }

  function markCLIBusy(id: string, action: CLIBusyAction) {
    setCliBusy((current) => ({ ...current, [id]: action }));
  }

  function clearCLIBusy(id: string) {
    setCliBusy((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  function forgetCLICheck(id: string) {
    setCliChecks((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  function beginCLIProgress(id: string, phase: string) {
    setCliProgress((current) => ({
      ...current,
      [id]: { phase, percent: 4, started_at: Date.now() },
    }));
  }

  function updateCLIProgress(id: string, progress: OperationProgress) {
    setCliProgress((current) => ({
      ...current,
      [id]: { ...progress, started_at: current[id]?.started_at ?? Date.now() },
    }));
  }

  function clearCLIProgress(id: string) {
    setCliProgress((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  async function checkCLIUpdate(id: string, silent = false) {
    markCLIBusy(id, "check");
    if (!silent) {
      setNotice("");
      setResult(null);
    }
    try {
      const res = await api.checkCLIUpdate(id);
      setCliChecks((current) => ({ ...current, [id]: res }));
      if (!silent) {
        if (res.error) {
          setNotice(`${t("tools.updateCheckFailed")}: ${res.error}`);
        } else if (res.update_available) {
          setNotice(`${t("tools.updateAvailable")}: ${res.current_version || "?"} -> ${res.latest_version || "?"}`);
        } else {
          setNotice(t("tools.upToDate"));
        }
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setCliChecks((current) => ({ ...current, [id]: { id, installed: true, update_available: false, error: message } }));
      if (!silent) setNotice(`${t("tools.updateCheckFailed")}: ${message}`);
    } finally {
      clearCLIBusy(id);
    }
  }

  async function installCLI(id: string, action: "install" | "update") {
    const item = cli.find((candidate) => candidate.spec.id === id);
    if (action === "install" && item?.spec.internal_only) {
      setInternalTarget({ kind: "cli", id, name: item.spec.name, action });
      return;
    }
    await performCLIInstall(id, action);
  }

  async function performCLIInstall(id: string, action: "install" | "update", acknowledgeInternal = false) {
    markCLIBusy(id, action);
    beginCLIProgress(id, action === "update" ? "checking" : "preparing");
    setNotice("");
    setResult(null);
    setBundleResult(null);
    if (action === "install") forgetCLICheck(id);
    try {
      const res = await api.installCLI(id, action, (progress) => updateCLIProgress(id, progress), acknowledgeInternal);
      setResult(res);
      setNotice(res.ok ? t("tools.cliReady") : res.error || t("tools.cliFailed"));
      await tools.reload();
      forgetCLICheck(id);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIProgress(id);
      clearCLIBusy(id);
    }
  }

  async function installBundle(bundle: ToolBundle, acknowledgeInternal = false) {
    if (bundle.spec.internal_only && !acknowledgeInternal) {
      setInternalTarget({
        kind: "bundle", id: bundle.spec.id, name: bundle.spec.name,
        components: bundle.spec.components.map((component) => component.name),
      });
      return;
    }
    const id = bundle.spec.id;
    setBundleBusy(id);
    setBundleResult(null);
    setResult(null);
    setNotice("");
    setBundleProgress((current) => ({ ...current, [id]: { phase: "preparing", percent: 4, started_at: Date.now() } }));
    try {
      const res = await api.installBundle(id, (progress) => {
        setBundleProgress((current) => ({
          ...current,
          [id]: { ...progress, started_at: current[id]?.started_at ?? Date.now() },
        }));
      }, acknowledgeInternal);
      setBundleResult(res);
      setNotice(res.ok ? t("tools.bundleReady") : res.error || t("tools.bundleFailed"));
      await tools.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBundleBusy("");
      setBundleProgress((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
    }
  }

  function confirmInternalInstall() {
    const target = internalTarget;
    setInternalTarget(null);
    if (!target) return;
    if (target.kind === "cli") {
      void performCLIInstall(target.id, target.action, true);
      return;
    }
    const bundle = bundles.find((candidate) => candidate.spec.id === target.id);
    if (bundle) void installBundle(bundle, true);
  }

  async function syncCLISkills(id: string) {
    markCLIBusy(id, "sync");
    beginCLIProgress(id, "preparing");
    setNotice("");
    setResult(null);
    try {
      const res = await api.syncCLISkills(id, (progress) => updateCLIProgress(id, progress));
      setResult(res);
      setNotice(res.ok ? t("tools.skillsSynced") : res.error || t("tools.skillsSyncFailed"));
      await tools.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIProgress(id);
      clearCLIBusy(id);
    }
  }

  async function startCLIAuth(id: string) {
    markCLIBusy(id, "auth");
    setNotice("");
    setResult(null);
    try {
      const session = await api.startCLIAuth(id, cliAuth[id]?.state === "authenticated");
      setCLIAuthSessions((current) => ({ ...current, [id]: session }));
      if (session.login_url && isDesktopApp()) {
        await openExternalURL(session.login_url);
      }
      if (cliAuthSessionTerminal(session.state)) {
        const status = await api.cliAuth(id);
        setCLIAuth((current) => ({ ...current, [id]: status }));
      } else {
        setNotice(session.phase === "setup" ? t("tools.authSetupWaiting") : t("tools.authLoginWaiting"));
      }
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIBusy(id);
    }
  }

  async function cancelCLIAuth(id: string, sessionID: string) {
    markCLIBusy(id, "auth");
    try {
      await api.cancelCLIAuth(sessionID);
      const snapshot = await api.cliAuthSession(sessionID);
      setCLIAuthSessions((current) => ({ ...current, [id]: snapshot }));
      setNotice(t("tools.authCancelled"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIBusy(id);
    }
  }

  async function installSkill(skill: MarketplaceSkill) {
    setBusy(`skill:${skill.name}`);
    setNotice("");
    setResult(null);
    try {
      await api.installSkill({ repo: skill.repo, path: skill.path, name: skill.name });
      setNotice(t("tools.skillInstalled"));
      await refreshAll();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function toggleSkill(skill: Skill) {
    setBusy(`toggle:${skill.name}`);
    try {
      await api.toggleSkill(skill.name, !skill.enabled);
      await refreshAll();
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="page-stack tools-page">
      <p className="subtle-copy">{t("tools.subtitle")}</p>
      {tools.error && <div className="surface-body error">{tools.error}</div>}
      {notice && <div className={`session-notice${(result && !result.ok) || (bundleResult && !bundleResult.ok) ? " error" : ""}`}>{notice}</div>}

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.bundleTitle")}</h2>
            <p className="subtle-copy">{t("tools.bundleSubtitle")}</p>
          </div>
        </div>
        <div className="tool-bundle-grid">
          {bundles.map((bundle) => (
            <article className={`tool-bundle-card${bundle.installed ? " installed" : ""}`} key={bundle.spec.id}>
              <header>
                <div>
                  <div className="catalog-badge-list">
                    <strong>{bundle.spec.name}</strong>
                    {bundle.spec.internal_only && <span className="status-badge warning internal-only-badge">{t("tools.internalBadge")}</span>}
                  </div>
                  <p>{bundle.spec.note}</p>
                </div>
                <span className={`status-badge ${bundle.installed ? "success" : ""}`}>
                  {bundle.installed ? <CheckCircle2 size={14} /> : <TriangleAlert size={14} />}
                  {bundle.ready_components} / {bundle.total_components}
                </span>
              </header>
              <div className="bundle-component-list">
                {bundle.components.map((component) => (
                  <span className={`status-badge ${component.ready ? "success" : ""}`} key={`${component.spec.kind}:${component.spec.id}`} title={component.detail}>
                    {component.ready ? <CheckCircle2 size={13} /> : <Download size={13} />}
                    {component.spec.name}{component.version ? ` · ${firstVersionLine(component.version)}` : ""}
                  </span>
                ))}
              </div>
              {bundle.detail && <div className="framework-hint"><TriangleAlert size={15} /><span>{bundle.detail}</span></div>}
              <footer>
                <button className="action" disabled={bundleBusy === bundle.spec.id || Boolean(bundle.detail)} onClick={() => void installBundle(bundle)} type="button">
                  <Download size={14} />
                  {bundleBusy === bundle.spec.id ? t("frameworks.installing") : bundle.installed ? t("tools.bundleRepair") : t("tools.bundleInstall")}
                </button>
              </footer>
              {bundleProgress[bundle.spec.id] && <OperationProgressView progress={bundleProgress[bundle.spec.id]} />}
            </article>
          ))}
          {bundles.length === 0 && <div className="empty-state">{t("tools.bundleEmpty")}</div>}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.cliTitle")}</h2>
            <p className="subtle-copy">{t("tools.cliSubtitle")}</p>
          </div>
          <button className="ghost-action" onClick={refreshAll}>
            <RefreshCw size={15} />
            {t("common.refresh")}
          </button>
        </div>
        <div className="catalog-table-wrap">
          <table className="catalog-table cli-catalog-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("common.description")}</th>
                <th>{t("tools.linkedSkills")}</th>
                <th>{t("common.status")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {cliPagination.pageItems.map((item) => (
                <CLIManagedRows
                  key={item.spec.id}
                  item={item}
                  targeted={item.spec.id === installTarget}
                  busy={cliBusy[item.spec.id]}
                  progress={cliProgress[item.spec.id]}
                  check={cliChecks[item.spec.id]}
                  auth={cliAuth[item.spec.id]}
                  authSession={cliAuthSessions[item.spec.id]}
                  onCheck={checkCLIUpdate}
                  onInstall={installCLI}
                  onSync={syncCLISkills}
                  onAuth={startCLIAuth}
                  onCancelAuth={cancelCLIAuth}
                  t={t}
                />
              ))}
            </tbody>
          </table>
        </div>
        <CatalogPagination
          page={cliPagination.page}
          totalPages={cliPagination.totalPages}
          start={cliPagination.start}
          end={cliPagination.end}
          total={cliPagination.total}
          onChange={cliPagination.setPage}
        />
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.marketTitle")}</h2>
            <p className="subtle-copy">{t("tools.marketSubtitle")}</p>
          </div>
          <label className="tools-search">
            <Search size={15} />
            <input value={marketQuery} onChange={(event) => setMarketQuery(event.target.value)} placeholder={t("tools.searchSkills")} />
          </label>
        </div>
        <div className="catalog-table-wrap">
          <table className="catalog-table skill-market-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("common.description")}</th>
                <th>{t("common.source")}</th>
                <th>{t("common.status")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {marketPagination.pageItems.map((skill) => (
                <MarketplaceRow
                  key={`${skill.repo}:${skill.path}`}
                  skill={skill}
                  busy={busy === `skill:${skill.name}`}
                  onInstall={installSkill}
                  t={t}
                />
              ))}
              {!marketplace.loading && market.length === 0 && (
                <tr>
                  <td className="empty-state" colSpan={5}>{t("tools.marketEmpty")}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <CatalogPagination
          page={marketPagination.page}
          totalPages={marketPagination.totalPages}
          start={marketPagination.start}
          end={marketPagination.end}
          total={marketPagination.total}
          onChange={marketPagination.setPage}
        />
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("tools.installedSkills")}</h2>
          <span className="pill on">{skills.length}</span>
        </div>
        <div className="catalog-table-wrap">
          <table className="catalog-table installed-skills-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("common.description")}</th>
                <th>{t("common.status")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {skillPagination.pageItems.map((skill) => (
                <tr className="catalog-row" key={skill.name}>
                  <td className="catalog-primary-cell" data-label={t("common.name")}>
                    <span className="provider-icon"><Bot size={16} /></span>
                    <span className="catalog-primary-copy"><strong>{skill.name}</strong></span>
                  </td>
                  <td className="catalog-description-cell" data-label={t("common.description")}>
                    {skill.description || t("common.description")}
                  </td>
                  <td data-label={t("common.status")}>
                    <span className={`status-badge ${skill.enabled ? "success" : ""}`}>
                      <span className="status-dot" />
                      {skill.enabled ? t("common.enabled") : t("common.disabled")}
                    </span>
                  </td>
                  <td className="catalog-action-cell" data-label={t("common.actions")}>
                    <button
                      className="ghost-action"
                      type="button"
                      onClick={() => toggleSkill(skill)}
                      disabled={busy === `toggle:${skill.name}`}
                    >
                      {busy === `toggle:${skill.name}`
                        ? t("common.loading")
                        : skill.enabled
                          ? t("common.disable")
                          : t("common.enable")}
                    </button>
                  </td>
                </tr>
              ))}
              {skills.length === 0 && (
                <tr>
                  <td className="empty-state" colSpan={4}>{t("skills.empty")}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <CatalogPagination
          page={skillPagination.page}
          totalPages={skillPagination.totalPages}
          start={skillPagination.start}
          end={skillPagination.end}
          total={skillPagination.total}
          onChange={skillPagination.setPage}
        />
      </section>

      {result?.log && (
        <section className="surface">
          <div className="surface-header">
            <h2>{t("tools.installLog")}</h2>
            {result.command && <span className="muted mono">{result.command}</span>}
          </div>
          <div className="surface-body">
            <pre className="framework-log">{result.log}</pre>
          </div>
        </section>
      )}
      {bundleResult && (
        <section className="surface">
          <div className="surface-header"><h2>{t("tools.bundleResult")}</h2></div>
          <div className="surface-body bundle-result-list">
            {(bundleResult.components ?? []).map((component) => (
              <div className={`bundle-result-item${component.ok ? " success" : " error"}`} key={`${component.kind}:${component.id}`}>
                <strong>{component.id}</strong>
                <span>{component.skipped ? t("tools.bundleSkipped") : component.ok ? t("tools.bundleComponentReady") : component.error}</span>
                {component.log && <pre className="framework-log">{component.log}</pre>}
              </div>
            ))}
          </div>
        </section>
      )}
      {internalTarget && (
        <InternalOnlyDialog
          name={internalTarget.name}
          components={internalTarget.kind === "bundle" ? internalTarget.components : []}
          onCancel={() => setInternalTarget(null)}
          onConfirm={confirmInternalInstall}
        />
      )}
    </div>
  );
}

function CLIManagedRows({
  item,
  targeted,
  busy,
  progress,
  check,
  auth,
  authSession,
  onCheck,
  onInstall,
  onSync,
  onAuth,
  onCancelAuth,
  t,
}: {
  item: CLIManagedTool;
  targeted: boolean;
  busy?: CLIBusyAction;
  progress?: OperationProgress;
  check?: CLIUpdateCheck;
  auth?: CLIAuthStatus;
  authSession?: CLIAuthSession;
  onCheck: (id: string) => void;
  onInstall: (id: string, action: "install" | "update") => void;
  onSync: (id: string) => void;
  onAuth: (id: string) => void;
  onCancelAuth: (id: string, sessionID: string) => void;
  t: (key: string) => string;
}) {
  const hasUpdate = Boolean(check?.update_available);
  const linkedSkills = item.linked_skills ?? [];
  const hasLinkedSkills = linkedSkills.length > 0 || Boolean(item.spec.linked_skills?.length);
  const needsSkillSync = linkedSkills.some((skill) => !skill.installed || !skill.in_sync);
  let action: "install" | "update" | "check" | "sync" = "check";
  if (!item.installed) action = "install";
  else if (hasUpdate) action = "update";
  else if (needsSkillSync) action = "sync";
  const authActive = Boolean(authSession && !cliAuthSessionTerminal(authSession.state));
  const disabled = Boolean(busy) || authActive;
  let buttonLabel = t("tools.checkUpdate");
  if (busy === "check") buttonLabel = t("tools.checkingUpdate");
  else if (busy === "sync") buttonLabel = t("tools.syncingSkills");
  else if (busy === "install" || busy === "update") buttonLabel = t("frameworks.installing");
  else if (action === "install") buttonLabel = hasLinkedSkills ? t("tools.installBundle") : t("frameworks.install");
  else if (action === "update") buttonLabel = t("tools.update");
  else if (action === "sync") buttonLabel = t("tools.syncSkills");
  const updateStatus = updateStatusLabel(check, t);
  const updateStatusClass = check?.error ? "warning" : check?.update_available ? "warning" : "success";
  const installedVersion = check?.current_version || firstVersionLine(item.version) || t("frameworks.installed");
  const authLabel = cliAuthStatusLabel(auth, t);
  const authAction = cliAuthActionLabel(auth, authSession, busy, t);
  return (
    <>
      <tr
        id={`cli-tool-${item.spec.id}`}
        className={`catalog-row${item.installed ? " installed" : ""}${targeted ? " install-target" : ""}`}
      >
        <td className="catalog-primary-cell" data-label={t("common.name")}>
          <span className="provider-icon"><TerminalSquare size={16} /></span>
          <span className="catalog-primary-copy">
            <strong>{item.spec.name}</strong>
            {item.spec.internal_only && <span className="status-badge warning internal-only-badge">{t("tools.internalBadge")}</span>}
            <small className="mono">{item.spec.bin}</small>
          </span>
        </td>
        <td className="catalog-description-cell" data-label={t("common.description")}>{item.spec.note || "—"}</td>
        <td data-label={t("tools.linkedSkills")}>
          <span className="catalog-badge-list">
            {linkedSkills.map((skill) => (
              <span
                key={skill.spec.id}
                className={`status-badge ${skill.installed && skill.in_sync ? "success" : "warning"}`}
                title={skill.detail || skill.spec.version_policy_label || skill.spec.note}
              >
                <Bot size={14} />
                <span className="status-badge-label">
                  {skill.spec.name}: {linkedSkillStatusLabel(skill, t)}
                </span>
              </span>
            ))}
            {linkedSkills.length === 0 && <span className="muted">—</span>}
          </span>
        </td>
        <td data-label={t("common.status")}>
        <span className="cli-status-stack">
          <span className={`status-badge ${item.installed ? "success" : ""}`} title={item.installed ? item.version : undefined}>
            {item.installed ? <CheckCircle2 size={14} /> : <TriangleAlert size={14} />}
            <span className="status-badge-label">
              {item.installed ? installedVersion : t("frameworks.notDetected")}
            </span>
          </span>
          {item.installed && updateStatus && (
            <span className={`status-badge ${updateStatusClass}`} title={updateStatus}>
              <span className="status-badge-label">{updateStatus}</span>
            </span>
          )}
          {item.installed && item.spec.login_supported && (
            <span
              className={`status-badge ${auth?.state === "authenticated" ? "success" : auth?.state === "unknown" ? "" : "warning"}`}
              title={auth?.detail}
            >
              <LogIn size={14} />
              <span className="status-badge-label">{authLabel}</span>
            </span>
          )}
        </span>
        </td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
          <div className="catalog-action-stack">
            <button
              className="action"
              disabled={disabled}
              onClick={() => {
                if (action === "check") onCheck(item.spec.id);
                else if (action === "sync") onSync(item.spec.id);
                else onInstall(item.spec.id, action);
              }}
            >
              {busy === "check" || action === "check" || action === "sync" ? <RefreshCw size={14} /> : <Download size={14} />}
              {buttonLabel}
            </button>
            {item.installed && item.spec.login_supported && (
              <button
                className="ghost-action"
                disabled={Boolean(busy) || authActive}
                onClick={() => onAuth(item.spec.id)}
                type="button"
              >
                <LogIn className={busy === "auth" ? "spin" : ""} size={14} />
                {authAction}
              </button>
            )}
          </div>
        </td>
      </tr>
      {progress && (
        <tr className="catalog-progress-row">
          <td colSpan={5}><OperationProgressView progress={progress} /></td>
        </tr>
      )}
      {authSession && (
        <tr className="catalog-progress-row cli-auth-row">
          <td colSpan={5}>
            <CLIAuthPrompt
              session={authSession}
              busy={busy === "auth"}
              onCancel={() => onCancelAuth(item.spec.id, authSession.session_id)}
              t={t}
            />
          </td>
        </tr>
      )}
    </>
  );
}

function CLIAuthPrompt({
  session,
  busy,
  onCancel,
  t,
}: {
  session: CLIAuthSession;
  busy: boolean;
  onCancel: () => void;
  t: (key: string) => string;
}) {
  const active = !cliAuthSessionTerminal(session.state);
  const phaseLabel = session.phase === "setup" ? t("tools.authSetupPhase") : t("tools.authLoginPhase");
  return (
    <div className={`cli-auth-prompt ${session.state}`}>
      <div className="cli-auth-prompt-copy">
        <strong>
          {session.state === "succeeded"
            ? t("tools.authReady")
            : session.state === "failed"
              ? t("tools.authFailed")
              : session.state === "cancelled"
                ? t("tools.authCancelled")
                : phaseLabel}
        </strong>
        {active && <span>{session.phase === "setup" ? t("tools.authSetupHelp") : t("tools.authLoginHelp")}</span>}
        {session.error && <span className="error">{session.error}</span>}
      </div>
      <div className="cli-auth-prompt-actions">
        {session.login_url && active && (
          <a
            className="action"
            href={session.login_url}
            onClick={(event) => {
              if (!isDesktopApp()) return;
              event.preventDefault();
              void openExternalURL(session.login_url || "");
            }}
            rel="noreferrer"
            target="_blank"
          >
            <ExternalLink size={14} />
            {t("tools.openAuthLink")}
          </a>
        )}
        {session.verification_code && active && (
          <span className="cli-auth-code">
            {t("tools.authCode")} <code>{session.verification_code}</code>
          </span>
        )}
        {active && (
          <button className="ghost-action" disabled={busy} onClick={onCancel} type="button">
            <X size={14} />
            {t("tools.authCancel")}
          </button>
        )}
      </div>
    </div>
  );
}

function cliAuthStatusLabel(auth: CLIAuthStatus | undefined, t: (key: string) => string) {
  if (!auth) return t("tools.authChecking");
  if (auth.state === "authenticated") return t("tools.authAuthenticated");
  if (auth.state === "setup_required") return t("tools.authSetupRequired");
  if (auth.state === "unauthenticated") return t("tools.authLoginRequired");
  return t("tools.authUnknown");
}

function cliAuthActionLabel(
  auth: CLIAuthStatus | undefined,
  session: CLIAuthSession | undefined,
  busy: CLIBusyAction | undefined,
  t: (key: string) => string,
) {
  if (busy === "auth") return t("tools.authStarting");
  if (session && !cliAuthSessionTerminal(session.state)) return t("tools.authWaiting");
  if (auth?.state === "authenticated") return t("tools.authAgain");
  if (auth?.state === "setup_required") return t("tools.authInitialize");
  return t("tools.authLogin");
}

function cliAuthSessionTerminal(state: CLIAuthSession["state"]) {
  return state === "succeeded" || state === "failed" || state === "cancelled";
}

function firstVersionLine(version?: string) {
  return version?.split(/\r?\n/, 1)[0]?.trim() || "";
}

function cliInstallTarget() {
  const match = window.location.hash.match(/^#\/?skills\/cli\/([^/?]+)/);
  if (!match) return "";
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return "";
  }
}

function linkedSkillStatusLabel(
  skill: { installed: boolean; in_sync: boolean; version?: string },
  t: (key: string) => string,
) {
  if (!skill.installed) return t("tools.skillMissing");
  if (!skill.in_sync) return `${skill.version || "?"} · ${t("tools.skillNeedsSync")}`;
  return `${skill.version || ""} · ${t("tools.skillInSync")}`.trim();
}

function updateStatusLabel(
  check: { error?: string; update_available: boolean; latest_version?: string } | undefined,
  t: (key: string) => string,
) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("tools.updateAvailable")} ${check.latest_version || ""}`.trim();
  return t("tools.upToDate");
}

function MarketplaceRow({
  skill,
  busy,
  onInstall,
  t,
}: {
  skill: MarketplaceSkill;
  busy: boolean;
  onInstall: (skill: MarketplaceSkill) => void;
  t: (key: string) => string;
}) {
  return (
    <tr className={`catalog-row${skill.installed ? " installed" : ""}`}>
      <td className="catalog-primary-cell" data-label={t("common.name")}>
        <span className="provider-icon"><Bot size={16} /></span>
        <span className="catalog-primary-copy">
          <strong>{skill.name}</strong>
          <small>{skill.category || skill.source}</small>
        </span>
      </td>
      <td className="catalog-description-cell" data-label={t("common.description")}>{skill.description || "—"}</td>
      <td data-label={t("common.source")}>
        <span className="catalog-source mono" title={`${skill.repo}/${skill.path}`}>{skill.source}</span>
      </td>
      <td data-label={t("common.status")}>
        <span className="catalog-badge-list">
          <span className={`status-badge ${skill.trusted ? "success" : ""}`}>
            {skill.trusted ? <ShieldCheck size={14} /> : <TriangleAlert size={14} />}
            {skill.trusted ? t("tools.trusted") : t("tools.community")}
          </span>
          {skill.installed && (
            <span className="status-badge success">
              <CheckCircle2 size={14} />
              {t("frameworks.installed")}
            </span>
          )}
        </span>
      </td>
      <td className="catalog-action-cell" data-label={t("common.actions")}>
        {!skill.installed && (
          <button className="action" disabled={busy} onClick={() => onInstall(skill)}>
            <Download size={14} />
            {busy ? t("frameworks.installing") : t("frameworks.install")}
          </button>
        )}
      </td>
    </tr>
  );
}
