export type SupportedCurrency = "cny" | "usd";

export function formatUsageCost(
  costUSD: number,
  currency: SupportedCurrency,
  cnyRate: number,
  language: "en" | "zh",
) {
  const code = currency === "cny" ? "CNY" : "USD";
  const value = currency === "cny" ? costUSD * validCNYRate(cnyRate) : costUSD;
  return new Intl.NumberFormat(language === "zh" ? "zh-CN" : "en-US", {
    style: "currency",
    currency: code,
    currencyDisplay: "symbol",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(value);
}

export function validCNYRate(value: number) {
  return Number.isFinite(value) && value > 0 ? value : 7;
}
