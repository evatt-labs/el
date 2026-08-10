// Opens a URL in the host's browser. Best-effort — a run that fails
// provisioning because it couldn't open a tab would be worse than one that
// just prints the URL and moves on, so failures here only warn.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

function isWsl() {
  if (process.platform !== "linux") return false;
  try {
    return readFileSync("/proc/version", "utf8").toLowerCase().includes("microsoft");
  } catch {
    return false;
  }
}

/**
 * Blocks non-http(s) schemes (file:, javascript:, etc.) from a malicious
 * open() hook. Deliberately NOT relied on to defeat shell metacharacters —
 * `&`, `|`, `^` are all legal inside a URL path or query string, so a
 * scheme check alone doesn't stop them from reaching whatever eventually
 * spawns the browser. See the WSL branch below for how that's actually
 * handled.
 */
function assertSafeHttpUrl(url) {
  const parsed = new URL(url);
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error(`Refusing to open non-http(s) URL: ${url}`);
  }
}

export function openUrl(url) {
  try {
    assertSafeHttpUrl(url);
    if (isWsl()) {
      // NOT `cmd.exe /c start "" <url>` — tested and confirmed exploitable:
      // cmd.exe's `start` re-parses the string it's handed as a shell
      // command line, so a URL like ".../health&ver" runs `ver` as a second
      // command. execFileSync's array-form argv doesn't protect against
      // this, because cmd.exe is a second shell layer past where that
      // guarantee applies.
      //
      // rundll32.exe url.dll,FileProtocolHandler takes the URL as a single
      // argument with no intermediate shell re-parsing — tested against the
      // same payload with no secondary command execution observed.
      execFileSync("rundll32.exe", ["url.dll,FileProtocolHandler", url], { stdio: "ignore" });
    } else if (process.platform === "darwin") {
      execFileSync("open", [url], { stdio: "ignore" });
    } else {
      execFileSync("xdg-open", [url], { stdio: "ignore" });
    }
  } catch (error) {
    console.warn(`  (couldn't open ${url} automatically — ${error.message ?? error})`);
  }
}
