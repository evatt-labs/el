// Parses `el`'s CLI arguments into a plain object bin/el.mjs can act on,
// so the grammar lives in one small, testable place instead of the manual
// `process.argv.slice(2)` destructure that used to accept anything.
//
// Exactly three forms are supported:
//
//   el up [name] [--output <path>] [--no-open]
//   el down <name>
//   el help | --help | -h
//
// Anything else (an unknown command, an unknown flag, a flag on `down`,
// more than one positional) throws a plain Error whose message is meant to
// be shown to a human, not parsed by code.

export function parseArgs(argv) {
  const [command, ...rest] = argv;

  if (command === undefined || command === "help" || command === "--help" || command === "-h") {
    return { command: "help", name: undefined, output: undefined, noOpen: false };
  }

  if (command !== "up" && command !== "down") {
    throw new Error(`Unknown command "${command}"`);
  }

  let name;
  let output;
  let noOpen = false;

  for (let i = 0; i < rest.length; i++) {
    const arg = rest[i];

    if (arg === "--output" || arg === "--no-open") {
      // Both flags exist only for `up`. On `down` they're simply unknown,
      // same message as any other unrecognized option.
      if (command !== "up") {
        throw new Error(`Unknown option "${arg}"`);
      }
      if (arg === "--output") {
        const value = rest[i + 1];
        if (value === undefined) {
          throw new Error('"--output" requires a value');
        }
        output = value;
        i++;
      } else {
        noOpen = true;
      }
      continue;
    }

    if (arg.startsWith("-")) {
      throw new Error(`Unknown option "${arg}"`);
    }

    if (name !== undefined) {
      throw new Error(`Unexpected argument "${arg}" (only one name may be given).`);
    }
    name = arg;
  }

  return { command, name, output, noOpen };
}
