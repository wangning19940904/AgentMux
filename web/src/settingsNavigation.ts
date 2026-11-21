import { Bell, Info, SlidersHorizontal } from "lucide-react";

export const SETTINGS_SECTIONS = [
  { id: "general", labelKey: "settings.general", icon: SlidersHorizontal },
  { id: "notifications", labelKey: "push.title", icon: Bell },
  { id: "about", labelKey: "settings.about", icon: Info },
] as const;

export type SettingsSection = typeof SETTINGS_SECTIONS[number]["id"];

export function settingsSectionFromHash(hash: string): SettingsSection {
  const [route, section] = hash.replace(/^#\/?/, "").split(/[/?]/);
  return SETTINGS_SECTIONS.find((item) => route === "settings" && item.id === section)?.id ?? "general";
}
