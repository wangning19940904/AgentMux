import { ChevronLeft, ChevronRight } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useI18n } from "../i18n";

export const CATALOG_PAGE_SIZE = 10;

export function useCatalogPagination<T>(items: T[], resetKey?: unknown) {
  const [page, setPage] = useState(1);
  const totalPages = Math.max(1, Math.ceil(items.length / CATALOG_PAGE_SIZE));

  useEffect(() => {
    setPage((current) => Math.min(current, totalPages));
  }, [totalPages]);

  useEffect(() => {
    if (resetKey !== undefined) setPage(1);
  }, [resetKey]);

  const startIndex = (page - 1) * CATALOG_PAGE_SIZE;
  const pageItems = useMemo(
    () => items.slice(startIndex, startIndex + CATALOG_PAGE_SIZE),
    [items, startIndex],
  );

  return {
    page,
    pageItems,
    setPage,
    start: items.length === 0 ? 0 : startIndex + 1,
    end: Math.min(startIndex + CATALOG_PAGE_SIZE, items.length),
    total: items.length,
    totalPages,
  };
}

export function CatalogPagination({
  page,
  totalPages,
  start,
  end,
  total,
  onChange,
}: {
  page: number;
  totalPages: number;
  start: number;
  end: number;
  total: number;
  onChange: (page: number) => void;
}) {
  const { t } = useI18n();
  if (total === 0) return null;

  return (
    <footer className="catalog-pagination">
      <span>{t("pagination.summary", { start, end, total })}</span>
      <div>
        <button
          className="ghost-action"
          type="button"
          disabled={page <= 1}
          onClick={() => onChange(page - 1)}
          aria-label={t("pagination.previous")}
        >
          <ChevronLeft size={15} />
          <span>{t("pagination.previous")}</span>
        </button>
        <strong>{t("pagination.page", { page, totalPages })}</strong>
        <button
          className="ghost-action"
          type="button"
          disabled={page >= totalPages}
          onClick={() => onChange(page + 1)}
          aria-label={t("pagination.next")}
        >
          <span>{t("pagination.next")}</span>
          <ChevronRight size={15} />
        </button>
      </div>
    </footer>
  );
}
