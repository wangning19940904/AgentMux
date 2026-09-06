export interface BulkActionResult {
  ok: boolean;
  error?: string;
  log?: string;
}

export interface BulkUpdateResult<T> {
  item: T;
  result: BulkActionResult;
}

export async function runBulkUpdates<T>(
  items: T[],
  update: (item: T) => Promise<BulkActionResult>,
  onResult: (entry: BulkUpdateResult<T>, completed: number) => void,
) {
  const results: BulkUpdateResult<T>[] = [];
  // Serialize package-manager writes, and keep going if one machine fails.
  for (const item of items) {
    let result: BulkActionResult;
    try {
      result = await update(item);
    } catch (error) {
      result = { ok: false, error: error instanceof Error ? error.message : String(error) };
    }
    const entry = { item, result };
    results.push(entry);
    onResult(entry, results.length);
  }
  return results;
}
