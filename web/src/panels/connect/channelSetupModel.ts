export function shouldAutoStartChannelSetup(
  draft: { id?: string; type?: string },
  startedPlatform: string,
): boolean {
  const platform = draft.type ?? "";
  return !draft.id &&
    (platform === "feishu" || platform === "lark") &&
    startedPlatform !== platform;
}
