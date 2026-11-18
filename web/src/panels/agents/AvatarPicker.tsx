import { Upload } from "lucide-react";
import { useRef, useState } from "react";
import type { Channel } from "../../api";
import { useI18n } from "../../i18n";
import { AgentAvatar } from "./AgentAvatar";
import { AVATAR_PRESETS, PresetAvatar, avatarFromFile, isImageAvatar, presetID, presetValue } from "./avatars";

// AvatarPicker chooses an agent's portrait: an uploaded image, one of the
// presets, or none, which shows the portrait of the bot it answers through.
export function AvatarPicker({ value, channels, onChange }: { value: string; channels: Channel[]; onChange: (value: string) => void }) {
  const { t } = useI18n();
  const input = useRef<HTMLInputElement>(null);
  const [error, setError] = useState("");
  const selectedPreset = presetID(value);
  const hasBot = channels.some((channel) => channel.bot_avatar_proxy_url || channel.bot_avatar_url);

  const upload = async (file: File | undefined) => {
    if (!file) return;
    setError("");
    try {
      onChange(await avatarFromFile(file));
    } catch (reason) {
      const code = reason instanceof Error ? reason.message : "";
      setError(t(code === "too-large" ? "superAgentForm.avatar.tooLarge" : code === "unsupported" ? "superAgentForm.avatar.unsupported" : "superAgentForm.avatar.unreadable"));
    } finally {
      if (input.current) input.current.value = "";
    }
  };

  return (
    <div className="field avatar-picker-field">
      <span id="saf-avatar-label">{t("superAgentForm.avatar")}</span>
      <div className="avatar-picker">
        <div className="avatar-picker-current">
          <AgentAvatar avatar={value} channels={channels} className="avatar-large" iconSize={26} />
          <small className="muted">
            {isImageAvatar(value) ? t("superAgentForm.avatar.uploaded")
              : selectedPreset ? t("superAgentForm.avatar.preset")
              : hasBot ? t("superAgentForm.avatar.bot") : t("superAgentForm.avatar.default")}
          </small>
        </div>
        <div className="avatar-picker-body">
          <div className="control-row">
            <button type="button" className="ghost-action" onClick={() => input.current?.click()}>
              <Upload size={14} /> {t("superAgentForm.avatar.upload")}
            </button>
            {value && (
              <button type="button" className="ghost-action" onClick={() => { setError(""); onChange(""); }}>
                {t("superAgentForm.avatar.useBot")}
              </button>
            )}
            <input
              ref={input}
              type="file"
              accept="image/png,image/jpeg,image/webp,image/gif"
              hidden
              aria-label={t("superAgentForm.avatar.upload")}
              onChange={(event) => void upload(event.target.files?.[0])}
            />
          </div>
          <div className="avatar-preset-grid" role="radiogroup" aria-labelledby="saf-avatar-label">
            {AVATAR_PRESETS.map((preset) => (
              <button
                key={preset.id}
                type="button"
                role="radio"
                aria-checked={selectedPreset === preset.id}
                aria-label={t("superAgentForm.avatar.presetNamed", { name: preset.id })}
                title={preset.id}
                className={`avatar-preset-option${selectedPreset === preset.id ? " selected" : ""}`}
                onClick={() => { setError(""); onChange(presetValue(preset.id)); }}
              >
                <PresetAvatar preset={preset} iconSize={17} />
              </button>
            ))}
          </div>
          {error ? <small className="avatar-picker-error" role="alert">{error}</small> : <small>{t("superAgentForm.avatar.help")}</small>}
        </div>
      </div>
    </div>
  );
}
