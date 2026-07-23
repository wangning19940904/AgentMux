import { useCallback, useEffect, useRef, useState } from "react";

const RETRY_DELAYS_MS = [250, 500, 1_000, 2_000, 4_000];

function isRetryableLoadError(error: unknown) {
  if (error instanceof TypeError) return true;
  const message = String(error);
  return (
    /load failed|failed to fetch|network(?:error| request failed)|fetch failed|connection refused/i.test(message) ||
    /:\s(?:408|425|429|500|502|503|504)$/.test(message)
  );
}

function delay(milliseconds: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, milliseconds));
}

// useAsync runs an async loader and tracks loading/error/data state, with a
// reload function for refresh-after-mutation.
export function useAsync<T>(loader: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(false);
  const requestID = useRef(0);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      requestID.current += 1;
    };
  }, []);

  const reload = useCallback(async () => {
    const currentRequest = ++requestID.current;
    setLoading(true);
    setError(null);

    for (let attempt = 0; ; attempt += 1) {
      try {
        const nextData = await loader();
        if (mounted.current && requestID.current === currentRequest) {
          setData(nextData);
          setError(null);
          setLoading(false);
        }
        return nextData;
      } catch (loadError) {
        if (!mounted.current || requestID.current !== currentRequest) return null;
        if (attempt < RETRY_DELAYS_MS.length && isRetryableLoadError(loadError)) {
          await delay(RETRY_DELAYS_MS[attempt]);
          continue;
        }
        setError(String(loadError));
        setLoading(false);
        return null;
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(() => {
    void reload();
  }, [reload]);

  return { data, error, loading, reload };
}
