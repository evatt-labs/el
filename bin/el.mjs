#!/usr/bin/env node
import { loadConfig } from "../src/config.mjs";
import { up } from "../src/up.mjs";
import { down } from "../src/down.mjs";

const HELP = `el — ephemeral full-stack preview environments on Cloudflare Workers + Neon

Usage:
  el up [name]     Spin up an environment. Generates a name if omitted.
  el down <name>   Tear down an environment by name.
  el help          Show this message.

Requires an el.config.mjs in the current directory, and NEON_API_KEY,
CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID in the environment or a .env
file. See https://github.com/evatt-labs/el for the config format.
`;

async function main() {
  const [command, arg] = process.argv.slice(2);

  if (command === undefined || command === "help" || command === "--help" || command === "-h") {
    console.log(HELP);
    return;
  }

  const config = await loadConfig();

  if (command === "up") {
    await up(config, arg);
    return;
  }
  if (command === "down") {
    await down(config, arg);
    return;
  }

  console.error(`Unknown command "${command}"\n`);
  console.log(HELP);
  process.exitCode = 1;
}

main().catch((error) => {
  console.error("\nel failed:", error.message ?? error);
  process.exitCode = 1;
});
