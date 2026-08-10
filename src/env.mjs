// Loads secrets from .env in the current working directory without ever
// printing them, and fails loudly and early if something required is
// missing — a provisioning run half-executing against undefined credentials
// is worse than one that refuses to start.

import { readFileSync } from "node:fs";
import path from "node:path";

function loadDotEnv() {
  let raw;
  try {
    raw = readFileSync(path.join(process.cwd(), ".env"), "utf8");
  } catch {
    return;
  }
  for (const line of raw.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "" || trimmed.startsWith("#")) continue;
    const eq = trimmed.indexOf("=");
    if (eq === -1) continue;
    const key = trimmed.slice(0, eq);
    let value = trimmed.slice(eq + 1);
    if (value.startsWith('"') && value.endsWith('"')) {
      value = value.slice(1, -1).replaceAll('\\"', '"');
    }
    process.env[key] ??= value;
  }
}

loadDotEnv();

export function requireEnv(...keys) {
  const missing = keys.filter((key) => !process.env[key]);
  if (missing.length > 0) {
    throw new Error(
      `Missing required environment variable(s): ${missing.join(", ")}. ` +
        `Set them in .env or export them before running.`,
    );
  }
  const values = {};
  for (const key of keys) values[key] = process.env[key];
  return values;
}
