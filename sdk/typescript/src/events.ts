/** Verify the original request body before parsing it; deduplicate IDs durably. */
export async function verifyEventSignature(
  secret: string,
  headers: { timestamp: string; deliveryId: string; signature: string },
  body: Uint8Array,
  nowMs = Date.now(),
): Promise<boolean> {
  if (
    !secret ||
    !headers.deliveryId ||
    !/^\d+$/.test(headers.timestamp) ||
    !/^v1=[a-f0-9]{64}$/.test(headers.signature)
  )
    return false;
  const seconds = Number(headers.timestamp);
  if (!Number.isSafeInteger(seconds) || Math.abs(nowMs / 1000 - seconds) > 300)
    return false;
  const encoder = new TextEncoder();
  const prefix = encoder.encode(`${headers.timestamp}.${headers.deliveryId}.`);
  const data = new Uint8Array(prefix.length + body.length);
  data.set(prefix);
  data.set(body, prefix.length);
  const signature = Uint8Array.from(
    headers.signature.slice(3).match(/../g)!,
    (value) => parseInt(value, 16),
  );
  const key = await globalThis.crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"],
  );
  return globalThis.crypto.subtle.verify("HMAC", key, signature, data);
}
