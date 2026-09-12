#!/usr/bin/env node
// Thin exec wrapper: forwards argv/stdio/exit-code to the real kraai binary
// that scripts/postinstall.js downloaded to ../.bin/kraai. No business logic
// lives here — same "thin entrypoint" principle as cmd/kraai/main.go.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const binPath = path.join(__dirname, "..", ".bin", "kraai");

if (!existsSync(binPath)) {
  console.error(
    "kraai: binary not found at " +
      binPath +
      ". `npm install` may have failed to download it — see the postinstall " +
      "output above, or re-run `npm install` for this package.",
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
  console.error(`kraai: failed to run ${binPath}: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status ?? 1);
