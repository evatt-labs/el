import { afterEach, describe, expect, it, vi } from "vitest";
import { waitForReachable } from "../src/reachability.mjs";

function textResponse(body) {
  return { text: () => Promise.resolve(body) };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("waitForReachable", () => {
  it("returns true immediately when the first response isn't an edge error page", async () => {
    const fetchMock = vi.fn().mockResolvedValue(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", { attempts: 5, delayMs: 0 });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("treats a real app's own error response as reachable, not as an edge error", async () => {
    // The whole point: el can't assume the app has any particular route, so
    // a 404 with the APP's own error shape must count as "reached the
    // Worker," not be confused with Cloudflare's edge fallback page.
    const fetchMock = vi
      .fn()
      .mockResolvedValue(textResponse('{"error":{"code":"not_found","message":"Route not found"}}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/nope", { attempts: 5, delayMs: 0 });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("retries past Cloudflare's edge-error page and succeeds once it clears", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(textResponse("error code: 1042\n"))
      .mockResolvedValueOnce(textResponse("error code: 1042\n"))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", { attempts: 5, delayMs: 0 });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("retries past a network-level failure the same way as an edge error", async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new Error("fetch failed"))
      .mockResolvedValueOnce(textResponse('{"status":"ok"}'));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", { attempts: 5, delayMs: 0 });

    expect(result).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("gives up and returns false — not throws — after exhausting attempts", async () => {
    const fetchMock = vi.fn().mockResolvedValue(textResponse("error code: 1042\n"));
    vi.stubGlobal("fetch", fetchMock);

    const result = await waitForReachable("https://example.workers.dev/status", { attempts: 3, delayMs: 0 });

    expect(result).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });
});
