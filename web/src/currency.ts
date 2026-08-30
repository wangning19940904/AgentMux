export type SupportedCurrency = "cny" | "usd";

export function formatUsageCost(
  costUSD: number,
  currency: SupportedCurrency,
  cnyRate: number,
  language: "en" | "zh",
) {
  const value = currency === "cny" ? costUSD * validCNYRate(cnyRate) : costUSD;
  const formattedValue = new Intl.NumberFormat(language === "zh" ? "zh-CN" : "en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(value);
  return `${currency === "cny" ? "¥" : "$"}${formattedValue}`;
}

export function validCNYRate(value: number) {
  return Number.isFinite(value) && value > 0 ? value : 7;
}
