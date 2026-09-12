// el's own version, read from package.json once at module load - static
// repo metadata, same as any other read like it, so no test here (matching
// the rest of the codebase's untested static-read files).

import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const packageJsonPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "package.json");
const packageJson = JSON.parse(readFileSync(packageJsonPath, "utf8"));

export const EL_VERSION = packageJson.version;
