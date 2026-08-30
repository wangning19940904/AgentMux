import { createContext, ReactNode, useContext } from "react";
import { en } from "./i18n/en";
import { zh } from "./i18n/zh";

export type Language = "en" | "zh";
export type ThemeMode = "system" | "light" | "dark";
export type MessageKey = keyof typeof en;

const messages: Record<Language, Record<MessageKey, string>> = { en, zh };

type Translate = (key: string, params?: Record<string, string | number>) => string;

const I18nContext = createContext<{ language: Language; t: Translate }>({
  language: "en",
  t: (key) => key,
});

export function I18nProvider({
  language,
  children,
}: {
  language: Language;
  children: ReactNode;
}) {
  function t(key: string, params?: Record<string, string | number>) {
    const messageKey = key as MessageKey;
    let value = messages[language][messageKey] ?? messages.en[messageKey] ?? key;
    if (params) {
      for (const [name, replacement] of Object.entries(params)) {
        value = value.split(`{${name}}`).join(String(replacement));
      }
    }
    return value;
  }

  return <I18nContext.Provider value={{ language, t }}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  return useContext(I18nContext);
}
