// The redacted diagnostics zip, built by this machine's daemon (the same
// bundle as `amux diagnose`). It is always local: a remote target would hand
// back that machine's bundle, which is not what Settings › About describes.

export type DiagnosticsFile = { name: string; size: number };

export type DiagnosticsDownload = {
  blob: Blob;
  filename: string;
  files: DiagnosticsFile[];
};

export async function fetchDiagnostics(includeContent: boolean): Promise<DiagnosticsDownload> {
  const path = `/api/v1/diagnostics${includeContent ? "?include_content=1" : ""}`;
  const res = await fetch(path, { cache: "no-store", headers: { "X-AgentMux-Console": "1" } });
  if (!res.ok) {
    const payload = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(payload.error || `${path}: ${res.status}`);
  }
  const disposition = res.headers.get("Content-Disposition") ?? "";
  const filename = /filename="?([^";]+)"?/.exec(disposition)?.[1] ?? "agentmux-diagnostics.zip";
  let files: DiagnosticsFile[] = [];
  try {
    files = JSON.parse(res.headers.get("X-AgentMux-Diagnostics-Files") ?? "[]") as DiagnosticsFile[];
  } catch {
    files = [];
  }
  return { blob: await res.blob(), filename, files };
}

// saveBlob hands a downloaded file to the browser's save flow.
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

export function formatFileSize(bytes: number): string {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(1)} KB`;
  return `${bytes} B`;
}
