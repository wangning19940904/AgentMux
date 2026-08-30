import { cp, rm } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "../..");
const source = resolve(root, "web/dist");
const target = resolve(here, "dist");

await rm(target, { recursive: true, force: true });
await cp(source, target, { recursive: true });
