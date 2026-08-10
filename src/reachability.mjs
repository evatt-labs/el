// Waits for a freshly-deployed Worker to actually be reachable across
// Cloudflare's edge before el considers provisioning done.
//
// Every environment `el up` creates gets a brand-new workers.dev hostname,
// never seen before — and Cloudflare's own docs are explicit that a first
// deploy to a new workers.dev subdomain can show errors "while DNS is
// propagating," which "should resolve themselves after a minute or so."
// This isn't a one-off; it's a property of the ephemeral-naming pattern
// itself; every `el up` run hits it. Without this wait, `up()` used to
// print "is live" and open a browser tab into exactly that window.

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Cloudflare's own edge fallback page for a request that hasn't finished
// propagating (or is otherwise misrouted at the edge) — verified
// empirically, not assumed: this exact plaintext body, "error code: 1042",
// was observed directly against a freshly-deployed workers.dev hostname.
// A real deployed application's response — even an error one — has its own
// shape (Hono's default 404, a JSON envelope, whatever the app defines);
// this specific pattern belongs to Cloudflare's edge, not to any Worker.
const EDGE_ERROR_PAGE = /^error code: \d+/i;

/**
 * Polls a URL until it stops returning Cloudflare's own edge-error page.
 * Best-effort: if the window elapses without becoming stable, this warns
 * rather than throwing — the environment is genuinely provisioned at this
 * point, and edge propagation will finish on its own even if it takes
 * longer than expected. Aborting the whole `up()` over that would be an
 * overreaction.
 */
export async function waitForReachable(url, { attempts = 20, delayMs = 3000 } = {}) {
  for (let i = 0; i < attempts; i++) {
    try {
      const response = await fetch(url);
      const body = await response.text();
      if (!EDGE_ERROR_PAGE.test(body.trim())) {
        return true;
      }
    } catch {
      // Network-level failure — DNS not yet resolving, connection refused.
      // Same "not ready yet" bucket as an edge-error page; just retry.
    }
    if (i < attempts - 1) await sleep(delayMs);
  }
  console.warn(
    `  (${url} still returning an edge error after ${String((attempts * delayMs) / 1000)}s — ` +
      `it should resolve on its own; this just means el didn't wait for it)`,
  );
  return false;
}
