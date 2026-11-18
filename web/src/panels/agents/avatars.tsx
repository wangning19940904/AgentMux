import type { LucideIcon } from "lucide-react";
import {
  Atom, Bird, Bot, Brain, Cat, Coffee, Compass, Dog, Fish, Flame, Gem, Ghost, Leaf, Lightbulb,
  Moon, Panda, Rabbit, Rocket, Sparkles, Squirrel, Sun, Telescope, Turtle, Zap,
} from "lucide-react";

// The Console's built-in portraits: an icon on a tinted tile. An agent stores
// one as "preset:<id>"; workers are given one at random.
export type AvatarPreset = { id: string; icon: LucideIcon; tone: number };

const presetIcons: [string, LucideIcon][] = [
  ["bot", Bot], ["sparkles", Sparkles], ["rocket", Rocket], ["brain", Brain],
  ["atom", Atom], ["telescope", Telescope], ["compass", Compass], ["lightbulb", Lightbulb],
  ["cat", Cat], ["dog", Dog], ["bird", Bird], ["fish", Fish],
  ["rabbit", Rabbit], ["turtle", Turtle], ["panda", Panda], ["squirrel", Squirrel],
  ["leaf", Leaf], ["flame", Flame], ["zap", Zap], ["sun", Sun],
  ["moon", Moon], ["gem", Gem], ["coffee", Coffee], ["ghost", Ghost],
];

export const AVATAR_TONES = 8;

// Neighbouring presets get different tones, so the grid reads as a palette.
export const AVATAR_PRESETS: AvatarPreset[] = presetIcons.map(([id, icon], index) => ({ id, icon, tone: (index * 3) % AVATAR_TONES }));

const PRESET_PREFIX = "preset:";

export function presetID(avatar: string | undefined): string {
  return avatar?.startsWith(PRESET_PREFIX) ? avatar.slice(PRESET_PREFIX.length) : "";
}

export function presetValue(id: string): string {
  return PRESET_PREFIX + id;
}

export function findPreset(id: string): AvatarPreset | undefined {
  return AVATAR_PRESETS.find((preset) => preset.id === id);
}

export function isImageAvatar(avatar: string | undefined): boolean {
  return /^data:image\/(png|jpeg|webp|gif);base64,/i.test(avatar ?? "");
}

export function PresetAvatar({ preset, className = "", iconSize = 15 }: { preset: AvatarPreset; className?: string; iconSize?: number }) {
  const Icon = preset.icon;
  return (
    <span className={`provider-icon agent-avatar avatar-preset avatar-tone-${preset.tone}${className ? ` ${className}` : ""}`} aria-hidden="true">
      <Icon size={iconSize} />
    </span>
  );
}

// Workers have no names of their own, so each is given one, with a portrait,
// drawn from its identity: the same worker is the same person in every chat,
// on every machine, without anything stored.
const WORKER_NAMES = [
  "Ada", "Atlas", "Bea", "Cleo", "Dex", "Eli", "Fern", "Finn", "Gus", "Hazel",
  "Iris", "Jade", "Juno", "Kai", "Leo", "Luna", "Milo", "Nova", "Olive", "Otto",
  "Pip", "Quinn", "Remy", "Rio", "Sage", "Theo", "Uma", "Vera", "Wren", "Zoe",
];

export type WorkerPersona = { name: string; preset: AvatarPreset };

export function workerKey(worker: { id?: string; agent_id?: string; host_id?: string }): string {
  return `${worker.host_id ?? ""}/${worker.agent_id || worker.id || ""}`;
}

function hash(text: string): number {
  // FNV-1a: small, stable and well spread for short keys.
  let value = 0x811c9dc5;
  for (let index = 0; index < text.length; index++) {
    value ^= text.charCodeAt(index);
    value = Math.imul(value, 0x01000193) >>> 0;
  }
  return value;
}

export function workerPersona(worker: { id?: string; agent_id?: string; host_id?: string }): WorkerPersona {
  const value = hash(workerKey(worker));
  // Name and portrait come from different bits, so they vary independently.
  return {
    name: WORKER_NAMES[value % WORKER_NAMES.length],
    preset: AVATAR_PRESETS[Math.floor(value / WORKER_NAMES.length) % AVATAR_PRESETS.length],
  };
}

export function WorkerFace({ worker, className = "", iconSize }: { worker: { id?: string; agent_id?: string; host_id?: string }; className?: string; iconSize?: number }) {
  return <PresetAvatar preset={workerPersona(worker).preset} className={className} iconSize={iconSize} />;
}

// avatarFromFile turns a picked image into a square portrait small enough to
// store with the agent.
export const AVATAR_PIXELS = 128;
export const MAX_AVATAR_UPLOAD = 8 * 1024 * 1024;

export async function avatarFromFile(file: File): Promise<string> {
  if (!/^image\/(png|jpeg|webp|gif)$/i.test(file.type)) throw new Error("unsupported");
  if (file.size > MAX_AVATAR_UPLOAD) throw new Error("too-large");
  const url = URL.createObjectURL(file);
  try {
    const image = await new Promise<HTMLImageElement>((resolve, reject) => {
      const element = new Image();
      element.onload = () => resolve(element);
      element.onerror = () => reject(new Error("unreadable"));
      element.src = url;
    });
    const side = Math.min(image.naturalWidth, image.naturalHeight);
    if (!side) throw new Error("unreadable");
    const canvas = document.createElement("canvas");
    canvas.width = AVATAR_PIXELS;
    canvas.height = AVATAR_PIXELS;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("unreadable");
    context.imageSmoothingQuality = "high";
    // The centre square, as profile pictures are cropped.
    context.drawImage(image, (image.naturalWidth - side) / 2, (image.naturalHeight - side) / 2, side, side, 0, 0, AVATAR_PIXELS, AVATAR_PIXELS);
    return canvas.toDataURL("image/png");
  } finally {
    URL.revokeObjectURL(url);
  }
}
