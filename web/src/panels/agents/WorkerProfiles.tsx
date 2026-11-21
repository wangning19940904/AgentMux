import { Gauge, Plus, Trash2 } from "lucide-react";
import type { SuperAgentWorkerProfile } from "../../api";
import { useI18n } from "../../i18n";
import { ModelPicker } from "../superAgentModelPicker";
import { profileProblem } from "../superAgentModel";

// WorkerProfiles edits an agent's worker profiles: model settings for auto
// workers of one framework. Each becomes roster entries auto:<framework>/<id>,
// and the router picks the one that suits how hard the work is.

type Props = {
  runtimes: { kind: string; display: string }[];
  value: SuperAgentWorkerProfile[];
  onChange: (next: SuperAgentWorkerProfile[]) => void;
};

// The two profiles most agents want: a strong one for hard work and a cheap
// one for simple work. Names are suggestions; the operator picks the models.
const STARTERS = [
  { id: "deep", reasoning_effort: "high", useKey: "superAgentForm.profileStarter.deep" },
  { id: "fast", reasoning_effort: "low", useKey: "superAgentForm.profileStarter.fast" },
];

export function WorkerProfiles({ runtimes, value, onChange }: Props) {
  const { t } = useI18n();
  const update = (index: number, patch: Partial<SuperAgentWorkerProfile>) =>
    onChange(value.map((profile, position) => position === index ? { ...profile, ...patch } : profile));
  const add = () => {
    const runtime = runtimes[0]?.kind ?? "";
    const taken = new Set(value.filter((profile) => profile.runtime_id === runtime).map((profile) => profile.id));
    const starter = STARTERS.find((item) => !taken.has(item.id));
    onChange([...value, starter
      ? { id: starter.id, runtime_id: runtime, reasoning_effort: starter.reasoning_effort, use: t(starter.useKey) }
      : { id: "", runtime_id: runtime }]);
  };

  return (
    <div className="field" role="group" aria-labelledby="saf-worker-profiles">
      <span id="saf-worker-profiles"><Gauge size={13} aria-hidden="true" /> {t("superAgentForm.workerProfiles")}</span>
      <small>{t("superAgentForm.workerProfilesHelp")}</small>
      {value.map((profile, index) => {
        const problem = profileProblem(profile, value);
        const labelID = `saf-profile-model-${index}`;
        return (
          <div className="super-agent-card super-agent-profile" key={index}>
            <div className="field-grid super-agent-path-grid">
              <label className="field"><span>{t("superAgentForm.profileFramework")}</span>
                <select value={profile.runtime_id} onChange={(event) => update(index, { runtime_id: event.target.value, model: "", reasoning_effort: "", service_tier: "" })}>
                  {runtimes.map((item) => <option key={item.kind} value={item.kind}>{item.display}</option>)}
                </select>
              </label>
              <label className="field"><span>{t("superAgentForm.profileID")}</span>
                <input value={profile.id} placeholder="deep" aria-invalid={problem === "superAgents.problem.profileID" || problem === "superAgents.problem.profileDuplicate"}
                  onChange={(event) => update(index, { id: event.target.value.toLowerCase() })} />
                <small><code>{`auto:${profile.runtime_id || "<framework>"}/${profile.id || "<id>"}`}</code></small>
              </label>
            </div>
            <div className="field">
              <span id={labelID}>{t("superAgentForm.profileModel")}</span>
              {profile.runtime_id ? (
                <ModelPicker runtime={profile.runtime_id} labelledBy={labelID}
                  value={{ model: profile.model ?? "", reasoning_effort: profile.reasoning_effort ?? "", service_tier: profile.service_tier ?? "" }}
                  onChange={(choice) => update(index, choice)} />
              ) : <small>{t("superAgents.modelChooseFramework")}</small>}
              <small>{t("superAgentForm.profileModelHelp")}</small>
            </div>
            <label className="field"><span>{t("superAgentForm.profileUse")}</span>
              <input value={profile.use ?? ""} placeholder={t("superAgentForm.profileUsePlaceholder")} onChange={(event) => update(index, { use: event.target.value })} />
            </label>
            <label className="field"><span>{t("superAgentForm.costTier")}</span>
              <select value={profile.cost_tier ?? ""} onChange={(event) => update(index, { cost_tier: event.target.value as SuperAgentWorkerProfile["cost_tier"] })}>
                <option value="">{t("superAgentForm.costTier.default")}</option>
                {(["low", "medium", "high"] as const).map((tier) => <option key={tier} value={tier}>{t(`superAgents.costTier.${tier}`)}</option>)}
              </select>
            </label>
            {problem && <small className="directory-notice" role="status">{t(problem)}</small>}
            <button className="ghost-action danger-action" type="button" onClick={() => onChange(value.filter((_, position) => position !== index))}><Trash2 size={13} /> {t("superAgents.remove")}</button>
          </div>
        );
      })}
      <button className="ghost-action super-agent-template-action" type="button" disabled={runtimes.length === 0} onClick={add}><Plus size={14} /> {t("superAgentForm.addProfile")}</button>
    </div>
  );
}
