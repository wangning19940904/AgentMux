import { createHmac, webcrypto } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { AgentMuxClient, verifyEventSignature } from "../src/index.js";
import type {
  RelayEvent,
  EventSubscription,
  EventDelivery,
  EventDeliveryAttempt,
  EventSource,
  EventIngestionHealth,
} from "../src/types.js";

if (!globalThis.crypto)
  Object.defineProperty(globalThis, "crypto", { value: webcrypto });

describe("events", () => {
  it("verifies exact bytes and rejects replay", async () => {
    const body = new TextEncoder().encode('{"id":"test"}');
    const timestamp = "2000000000",
      deliveryId = "delivery";
    const signature =
      "v1=" +
      createHmac("sha256", "secret")
        .update(`${timestamp}.${deliveryId}.`)
        .update(body)
        .digest("hex");
    expect(
      await verifyEventSignature(
        "secret",
        { timestamp, deliveryId, signature },
        body,
        2000000000000,
      ),
    ).toBe(true);
    expect(
      await verifyEventSignature(
        "secret",
        { timestamp, deliveryId, signature },
        body,
        2000000301000,
      ),
    ).toBe(false);
    expect(
      await verifyEventSignature(
        "secret",
        { timestamp, deliveryId: "other", signature },
        body,
        2000000000000,
      ),
    ).toBe(false);
  });
  it("exposes subscription registration and delivery management", async () => {
    const calls: string[] = [];
    const client = new AgentMuxClient({
      token: "tenant",
      fetch: async (input, init) => {
        const url = new URL(String(input));
        calls.push(`${init?.method ?? "GET"} ${url.pathname}`);
        if (url.pathname.endsWith("rotate-secret"))
          return Response.json({ signing_secret: "rotated" });
        if (
          url.pathname.endsWith("event-subscriptions") &&
          init?.method === "POST"
        )
          return Response.json({
            subscription: { id: "sub" },
            signing_secret: "once",
          });
        return Response.json([]);
      },
    });
    expect(await client.events.sources()).toEqual([]);
    expect(
      (
        await client.events.upsert({
          key: "homebook",
          name: "Homebook",
          source: "feishu",
          source_refs: ["channel:ch"],
          event_types: ["changed"],
          callback_url: "http://127.0.0.1/events",
        })
      ).signing_secret,
    ).toBe("once");
    await client.events.list();
    await client.events.test("sub");
    expect(await client.events.rotateSecret("sub")).toBe("rotated");
    await client.events.deliveries({ limit: 25 });
    await client.events.retry("d");
    await client.events.delete("sub");
    expect(calls).toHaveLength(8);
  });
});

it("relay_event matches the Go contract", () => {
  const fields: Record<keyof RelayEvent, boolean> = {
    attributes: true,
    data: true,
    id: true,
    occurred_at: true,
    received_at: true,
    schema_version: true,
    source: true,
    source_event_id: true,
    source_ref: true,
    type: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL("../../../contract/schemas/relay_event.json", import.meta.url),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});

it("event_subscription matches the Go contract", () => {
  const fields: Record<keyof EventSubscription, boolean> = {
    callback_url: true,
    created_at: true,
    dead: true,
    deleted: true,
    event_types: true,
    filters: true,
    id: true,
    key: true,
    last_error: true,
    last_success_at: true,
    name: true,
    owner_tenant_id: true,
    paused: true,
    pending: true,
    source: true,
    source_refs: true,
    updated_at: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL(
        "../../../contract/schemas/event_subscription.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});

it("event_delivery matches the Go contract", () => {
  const fields: Record<keyof EventDelivery, boolean> = {
    attempts: true,
    created_at: true,
    event: true,
    event_id: true,
    first_attempt_at: true,
    history: true,
    id: true,
    last_error: true,
    next_attempt_at: true,
    status: true,
    subscription_id: true,
    updated_at: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL("../../../contract/schemas/event_delivery.json", import.meta.url),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});

it("event_delivery_attempt matches the Go contract", () => {
  const fields: Record<keyof EventDeliveryAttempt, boolean> = {
    delivery_id: true,
    error: true,
    finished_at: true,
    http_status: true,
    id: true,
    started_at: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL(
        "../../../contract/schemas/event_delivery_attempt.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});

it("event_source matches the Go contract", () => {
  const fields: Record<keyof EventSource, boolean> = {
    connected: true,
    event_types: true,
    ingestion: true,
    name: true,
    platform_verified: true,
    ref: true,
    sources: true,
    state: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL("../../../contract/schemas/event_source.json", import.meta.url),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});

it("event_ingestion_health matches the Go contract", () => {
  const fields: Record<keyof EventIngestionHealth, boolean> = {
    failures: true,
    last_error: true,
    last_failure_at: true,
    last_success_at: true,
  };
  const schema = JSON.parse(
    readFileSync(
      new URL(
        "../../../contract/schemas/event_ingestion_health.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  expect(Object.keys(fields).sort()).toEqual(Object.keys(schema.fields).sort());
});
