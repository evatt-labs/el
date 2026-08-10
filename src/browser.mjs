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

export function openUrl(url) {
  try {
    if (isWsl()) {
      // cmd.exe's `start` treats the first quoted argument as a window
      // title, not the target — the empty "" is required, not decorative.
      execFileSync("cmd.exe", ["/c", "start", "", url], { stdio: "ignore" });
    } else if (process.platform === "darwin") {
      execFileSync("open", [url], { stdio: "ignore" });
    } else {
      execFileSync("xdg-open", [url], { stdio: "ignore" });
    }
  } catch (error) {
    console.warn(`  (couldn't open ${url} automatically — ${error.message ?? error})`);
  }
}
