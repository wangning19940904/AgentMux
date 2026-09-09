import { useI18n } from "../i18n";

export function MachineInstances({ instances, selected, onSelect }: {
  instances: { key: string; targetID?: string; name?: string; detail?: string }[];
  selected: string;
  onSelect: (key: string) => void;
}) {
  const { t } = useI18n();
  if (instances.length < 2) return null;
  return <span className="machine-instances">
    <span className="muted">{t("resources.machineCount", { count: instances.length })}</span>
    <span className="machine-instance-options" role="group" aria-label={t("resources.selectMachine")}>
      {instances.map((item) => <button key={item.key} type="button"
        className="machine-instance-option" aria-pressed={item.key === selected}
        onClick={() => onSelect(item.key)}>
        {(!item.targetID || item.targetID === "local") ? t("remote.localMachine") : item.name || item.targetID}
        {item.detail && <small>{item.detail}</small>}
      </button>)}
    </span>
    <small className="muted">{t("resources.selectedMachineHint")}</small>
  </span>;
}
