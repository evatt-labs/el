import { afterEach, describe, expect, it, vi } from "vitest";
import { waitForReachable } from "../src/reachability.mjs";

function textResponse(body) {
  return { text: () => Promise.resolve(body) };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("waitForReachable", () => {
  it("returns true once it sees the required number of consecutive successes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", {
      attempts: 10,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("treats a real app's own error response as reachable, not as an edge error", async () => {
    // The whole point: el can't assume the app has any particular route, so
    // a 404 with the APP's own error shape must count as "reached the
    // Worker," not be confused with Cloudflare's edge fallback page.
    const fetchMock = vi
      .fn()
      .mockResolvedValue(textResponse('{"error":{"code":"not_found","message":"Route not found"}}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/nope", {
      attempts: 10,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(true);
  });

  it("resets the streak on a single edge-error response, even after prior successes", async () => {
    // This is the actual behavior a single successful check misses: a URL
    // can answer correctly, then hit the edge-error page on the very next
    // request, then correctly again — one different Cloudflare PoP per
    // request. A flaky success followed by a failure must not count.
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse("error code: 1042\n")) // resets the streak
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", {
      attempts: 10,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(6);
  });

  it("resets the streak on a network-level failure the same way as an edge error", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockRejectedValueOnce(new Error("fetch failed")) // resets the streak
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", {
      attempts: 10,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(5);
  });

  it("gives up and returns false — not throws — after exhausting attempts without reaching the streak", async () => {
    const fetchMock = vi.fn().mockResolvedValue(textResponse("error code: 1042\n"));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", {
      attempts: 5,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(5);
  });

  it("gives up even if it was one success away from the required streak", async () => {
    // 2 consecutive successes, then attempts run out before the 3rd.
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", {
      attempts: 2,
      delayMs: 0,
      consecutiveSuccesses: 3,
    });

    expect(result).toBe(false);
  });
});
