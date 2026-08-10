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

async function probe(url) {
  try {
    const response = await fetch(url);
    const body = await response.text();
    return !EDGE_ERROR_PAGE.test(body.trim());
  } catch {
    // Network-level failure — DNS not yet resolving, connection refused.
    // Same "not ready yet" bucket as an edge-error page.
    return false;
  }
}

/**
 * Polls a URL until it stops returning Cloudflare's own edge-error page —
 * for `consecutiveSuccesses` requests in a row, not just once.
 *
 * One success is not enough: verified live, a URL can answer correctly,
 * then answer with the edge-error page on the very next request, then
 * correctly again. Cloudflare's anycast network routes different requests
 * to different points of presence, and they don't all learn about a new
 * hostname at the same moment — one successful probe only proves the ONE
 * PoP that particular request happened to land on is ready, not that all of
 * them are. Requiring several in a row makes it far less likely every one
 * of them landed on the same not-yet-propagated PoP by chance, without
 * pretending this proves *global* consistency, which no client sitting
 * behind anycast routing can actually observe from one vantage point.
 *
 * Best-effort: if the window elapses without reaching that streak, this
 * warns rather than throwing — the environment is genuinely provisioned at
 * this point, and propagation will finish on its own even if it takes
 * longer than expected or than this function can confirm. A consumer that
 * depends on this being airtight (a CI smoke test, not a human clicking a
 * link) should still carry its own retry — this reduces the odds of
 * hitting the window, it does not eliminate them.
 */
export async function waitForReachable(
  url,
  { attempts = 20, delayMs = 3000, consecutiveSuccesses = 3 } = {},
) {
  let streak = 0;
  for (let i = 0; i < attempts; i++) {
    streak = (await probe(url)) ? streak + 1 : 0;
    if (streak >= consecutiveSuccesses) return true;
    if (i < attempts - 1) await sleep(delayMs);
  }
  console.warn(
    `  (${url} did not settle after ${String((attempts * delayMs) / 1000)}s — it should still ` +
      `resolve on its own; a consumer that depends on this being ready immediately, like a CI ` +
      `smoke test, should carry its own retry too)`,
  );
  return false;
}
