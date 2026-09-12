#!/usr/bin/env node
import { loadConfig } from "../src/config.mjs";
import { up } from "../src/up.mjs";
import { down } from "../src/down.mjs";
import { parseArgs } from "../src/cli-args.mjs";

const HELP = `el: ephemeral full-stack preview environments on Cloudflare Workers

Usage:
  el up [name] [--output <path>] [--no-open]
                   Spin up an environment. Generates a name if omitted.
                   --output <path>  Write a JSON summary (name, urls,
                                     summary, seed) to path after it's up.
                   --no-open        Skip the open() hook entirely.
  el down <name>   Tear down an environment by name.
  el help          Show this message.

Requires an el.config.mjs in the current directory, and CLOUDFLARE_API_TOKEN,
CLOUDFLARE_ACCOUNT_ID in the environment or a .env file. With no top-level
"database" block, that's all el needs: it's D1-only. Configuring a database
provider (Neon is the only built-in) adds that provider's own required env
vars, e.g. NEON_API_KEY. See https://github.com/evatt-labs/el for the config
format.

el.config.mjs runs as ordinary Node code with full access to your
environment (including CLOUDFLARE_API_TOKEN and any database provider
credentials). Only run \`el up\`/\`el down\` against a config you trust.
Never wire this into a workflow that runs against an untrusted fork's
branch.
`;

async function main() {
  let args;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (error) {
    console.error(`${error.message}\n`);
    console.log(HELP);
    process.exitCode = 1;
    return;
  }

  if (args.command === "help") {
    console.log(HELP);
    return;
  }

  // el.config.mjs is arbitrary code in the current directory (that's the
  // point, it's the extension mechanism) so it's only loaded once the
  // command actually needs it, not for an argument error above.
  if (args.command === "up") {
    await up(await loadConfig(), args.name, { output: args.output, noOpen: args.noOpen });
    return;
  }
  await down(await loadConfig(), args.name);
}

main().catch((error) => {
  console.error("\nel failed:", error.message ?? error);
  process.exitCode = 1;
});
