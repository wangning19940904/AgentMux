import { CirclePlus } from "lucide-react";
import { iconForProvider } from "./providerUtils";

export function ProviderMark({
  id,
  name,
  size = "small",
  custom = false,
}: {
  id: string;
  name?: string;
  size?: "small" | "large" | "avatar";
  custom?: boolean;
}) {
  const Icon = custom ? CirclePlus : iconForProvider(id);
  const iconSize = size === "avatar" ? 34 : size === "large" ? 18 : 14;
  const className = size === "avatar" ? "provider-avatar" : size === "large" ? "provider-icon large" : "provider-icon";

  return (
    <span className={className} title={name}>
      <Icon size={iconSize} />
    </span>
  );
}
export function CapabilityBadges({ items }: { items: string[] }) {
  const displayItems = items.length > 0 ? items : ["—"];
  return (
    <>
      {displayItems.map((item) => (
        <span className="pill" key={item}>
          {item}
        </span>
      ))}
    </>
  );
}
