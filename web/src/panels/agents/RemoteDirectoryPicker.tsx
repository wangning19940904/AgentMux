import { ArrowUp, Folder, FolderOpen, FolderPlus, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { api, type SystemDirectoryListing } from "../../api";
import { workDirErrorMessage } from "./agentUtils";


export function RemoteDirectoryPicker({
  initialPath,
  targetID,
  onClose,
  onSelect,
  t,
}: {
  initialPath: string;
  targetID?: string;
  onClose: () => void;
  onSelect: (path: string) => void;
  t: (key: string) => string;
}) {
  const [listing, setListing] = useState<SystemDirectoryListing | null>(null);
  const [path, setPath] = useState(initialPath);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [newDirectoryName, setNewDirectoryName] = useState("");
  const [createError, setCreateError] = useState("");

  async function openPath(nextPath: string, fallbackToHome = false) {
    setBusy(true);
    setError("");
    setListing(null);
    setCreateOpen(false);
    setNewDirectoryName("");
    setCreateError("");
    try {
      let next: SystemDirectoryListing;
      try {
        next = await api.directories(nextPath, targetID);
      } catch (err) {
        if (!fallbackToHome || !nextPath.trim()) throw err;
        next = await api.directories("", targetID);
      }
      setListing(next);
      setPath(next.path);
    } catch (err) {
      setError(workDirErrorMessage(err, t));
    } finally {
      setBusy(false);
    }
  }

  async function createDirectory() {
    if (!listing || busy) return;
    const name = newDirectoryName.trim();
    if (!name) {
      setCreateError(t("agents.remoteWorkDirNameRequired"));
      return;
    }
    if (name === "." || name === ".." || name.includes("/")) {
      setCreateError(t("agents.remoteWorkDirNameInvalid"));
      return;
    }

    setBusy(true);
    setError("");
    setCreateError("");
    const basePath = listing.path === "/" ? "" : listing.path.replace(/\/+$/, "");
    try {
      const created = await api.ensureDirectory(`${basePath}/${name}`, targetID);
      await openPath(created.path);
    } catch (err) {
      setCreateError(workDirErrorMessage(err, t));
      setBusy(false);
    }
  }

  useEffect(() => {
    void openPath(initialPath, true);
  }, []);

  useEffect(() => {
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      if (createOpen) {
        setCreateOpen(false);
        setNewDirectoryName("");
        setCreateError("");
        return;
      }
      onClose();
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [createOpen, onClose]);

  return createPortal(
    <div className="remote-directory-picker-layer">
      <button
        aria-label={t("common.close")}
        className="remote-directory-picker-backdrop"
        onClick={onClose}
        type="button"
      />
      <section
        aria-labelledby="remote-directory-picker-title"
        aria-modal="true"
        className="remote-directory-picker"
        role="dialog"
      >
        <header className="remote-directory-picker-head">
          <div>
            <h3 id="remote-directory-picker-title">{t("agents.remoteWorkDirTitle")}</h3>
            <p>{t("agents.remoteWorkDirHint")}</p>
          </div>
          <button className="ghost-action icon-action" onClick={onClose} title={t("common.close")} type="button">
            <X size={15} />
          </button>
        </header>

        <form
          className="remote-directory-path-bar"
          onSubmit={(event) => {
            event.preventDefault();
            void openPath(path);
          }}
        >
          <input
            aria-label={t("agents.remoteWorkDirPath")}
            autoFocus
            onChange={(event) => {
              setPath(event.target.value);
              setCreateOpen(false);
              setNewDirectoryName("");
              setCreateError("");
            }}
            placeholder={t("agents.remoteWorkDirPath")}
            spellCheck={false}
            value={path}
          />
          <button className="ghost-action" disabled={busy} type="submit">
            <FolderOpen size={15} />
            {t("agents.remoteWorkDirOpen")}
          </button>
          <button
            className="ghost-action"
            disabled={busy || !listing || path.trim() !== listing.path}
            onClick={() => {
              setCreateOpen(true);
              setNewDirectoryName("");
              setCreateError("");
            }}
            type="button"
          >
            <FolderPlus size={15} />
            {t("agents.remoteWorkDirCreate")}
          </button>
        </form>

        <div className="remote-directory-list">
          {createOpen && listing && (
            <form
              className="remote-directory-create-form"
              onSubmit={(event) => {
                event.preventDefault();
                void createDirectory();
              }}
            >
              <FolderPlus size={17} />
              <input
                aria-label={t("agents.remoteWorkDirCreatePlaceholder")}
                autoFocus
                disabled={busy}
                onChange={(event) => {
                  setNewDirectoryName(event.target.value);
                  setCreateError("");
                }}
                placeholder={t("agents.remoteWorkDirCreatePlaceholder")}
                spellCheck={false}
                value={newDirectoryName}
              />
              <button className="action" disabled={busy || !newDirectoryName.trim()} type="submit">
                {t("agents.remoteWorkDirCreate")}
              </button>
              <button
                aria-label={t("common.close")}
                className="ghost-action icon-action"
                disabled={busy}
                onClick={() => {
                  setCreateOpen(false);
                  setNewDirectoryName("");
                  setCreateError("");
                }}
                title={t("common.close")}
                type="button"
              >
                <X size={14} />
              </button>
              {createError && (
                <small className="remote-directory-create-error" role="alert">
                  {createError}
                </small>
              )}
            </form>
          )}
          {listing?.parent_path && (
            <button
              className="remote-directory-row parent"
              disabled={busy}
              onClick={() => void openPath(listing.parent_path ?? "")}
              type="button"
            >
              <ArrowUp size={16} />
              <span>
                <strong>{t("agents.remoteWorkDirParent")}</strong>
                <small>{listing.parent_path}</small>
              </span>
            </button>
          )}
          {busy && <div className="remote-directory-state">{t("common.loading")}</div>}
          {!busy && error && <div className="session-notice error">{error}</div>}
          {!busy && !error && listing?.entries.length === 0 && (
            <div className="remote-directory-state">{t("agents.remoteWorkDirEmpty")}</div>
          )}
          {!busy && !error && listing?.entries.map((entry) => (
            <button
              className="remote-directory-row"
              disabled={busy}
              key={entry.path}
              onClick={() => void openPath(entry.path)}
              type="button"
            >
              <Folder size={16} />
              <span>
                <strong>{entry.name}</strong>
                <small>{entry.path}</small>
              </span>
            </button>
          ))}
        </div>

        <footer className="remote-directory-picker-actions">
          <span title={listing?.path}>{listing?.path ?? ""}</span>
          <div>
            <button className="ghost-action" onClick={onClose} type="button">
              {t("common.close")}
            </button>
            <button
              className="action"
              disabled={!listing || busy}
              onClick={() => listing && onSelect(listing.path)}
              type="button"
            >
              <FolderOpen size={15} />
              {t("agents.remoteWorkDirChoose")}
            </button>
          </div>
        </footer>
      </section>
    </div>,
    document.body,
  );
}
