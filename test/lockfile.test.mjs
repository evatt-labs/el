import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { deleteLock, emptyLock, lockPath, mergeResources, readLock, writeLock } from "../src/lockfile.mjs";

let cwd;

beforeEach(() => {
  cwd = mkdtempSync(path.join(tmpdir(), "kraai-lockfile-test-"));
});

afterEach(() => {
  rmSync(cwd, { recursive: true, force: true });
});

describe("lockPath", () => {
  it("builds .kraai/<name>.lock.json under the given directory", () => {
    expect(lockPath("/tmp/proj", "blue-honey-badger-12345")).toBe(
      path.join("/tmp/proj", ".kraai", "blue-honey-badger-12345.lock.json"),
    );
  });
});

describe("emptyLock", () => {
  it("has the initial shape, with database and services empty", () => {
    const lock = emptyLock({
      name: "blue-honey-badger-12345",
      elVersion: "0.5.0",
      accountId: "acct-1",
      subdomain: "acme",
    });
    expect(lock).toEqual({
      lockfileVersion: 1,
      name: "blue-honey-badger-12345",
      createdAt: expect.any(String),
      elVersion: "0.5.0",
      accountId: "acct-1",
      subdomain: "acme",
      database: null,
      services: {},
    });
    expect(() => new Date(lock.createdAt).toISOString()).not.toThrow();
  });
});

describe("writeLock / readLock", () => {
  it("round trips a lock through the filesystem", () => {
    const lock = emptyLock({ name: "n", elVersion: "0.5.0", accountId: "a", subdomain: "s" });
    lock.services.api = { dir: "packages/api" };
    writeLock(cwd, lock);
    expect(readLock(cwd, "n")).toEqual(lock);
  });

  it("creates .kraai/ if it doesn't exist yet", () => {
    const lock = emptyLock({ name: "n", elVersion: "0.5.0", accountId: "a", subdomain: "s" });
    writeLock(cwd, lock);
    expect(readFileSync(lockPath(cwd, "n"), "utf8")).toContain('"lockfileVersion": 1');
  });

  it("overwrites on repeated writes rather than appending", () => {
    const lock = emptyLock({ name: "n", elVersion: "0.5.0", accountId: "a", subdomain: "s" });
    writeLock(cwd, lock);
    lock.services.api = { dir: "packages/api" };
    writeLock(cwd, lock);
    expect(readLock(cwd, "n").services).toEqual({ api: { dir: "packages/api" } });
  });

  it("returns undefined for a missing lockfile", () => {
    expect(readLock(cwd, "does-not-exist")).toBeUndefined();
  });

  it("throws on a lockfile that isn't valid JSON", () => {
    mkdirSync(path.join(cwd, ".kraai"), { recursive: true });
    writeFileSync(lockPath(cwd, "n"), "{ not json", { flag: "wx" });
    expect(() => readLock(cwd, "n")).toThrow(/not valid JSON/);
  });

  it("throws on a lockfile with an unexpected lockfileVersion", () => {
    const lock = emptyLock({ name: "n", elVersion: "0.5.0", accountId: "a", subdomain: "s" });
    writeLock(cwd, { ...lock, lockfileVersion: 2 });
    expect(() => readLock(cwd, "n")).toThrow(/lockfileVersion/);
  });
});

describe("deleteLock", () => {
  it("removes an existing lockfile", () => {
    const lock = emptyLock({ name: "n", elVersion: "0.5.0", accountId: "a", subdomain: "s" });
    writeLock(cwd, lock);
    deleteLock(cwd, "n");
    expect(readLock(cwd, "n")).toBeUndefined();
  });

  it("doesn't throw when the lockfile is already gone", () => {
    expect(() => deleteLock(cwd, "does-not-exist")).not.toThrow();
  });
});

describe("mergeResources", () => {
  it("returns empty arrays for every type when both sides are undefined", () => {
    expect(mergeResources(undefined, undefined, "n")).toEqual({ d1: [], kv: [], r2: [], queues: [] });
  });

  it("includes a resource recorded in the lock but no longer declared in config", () => {
    const lockService = { resources: { d1: [{ binding: "DB", name: "n-api-db" }] } };
    const result = mergeResources(lockService, undefined, "n");
    expect(result.d1).toEqual([{ binding: "DB", name: "n-api-db" }]);
  });

  it("includes a resource declared in config with no lockfile at all", () => {
    const configService = { key: "api", d1: [{ binding: "DB" }] };
    const result = mergeResources(undefined, configService, "n");
    expect(result.d1).toEqual([{ binding: "DB", name: "n-api-db" }]);
  });

  it("counts a resource present in both exactly once", () => {
    const lockService = { resources: { d1: [{ binding: "DB", name: "n-api-db" }] } };
    const configService = { key: "api", d1: [{ binding: "DB" }] };
    const result = mergeResources(lockService, configService, "n");
    expect(result.d1).toEqual([{ binding: "DB", name: "n-api-db" }]);
  });

  it("dedupes by name even when the lock and config disagree on binding", () => {
    // The binding that produced "n-api-db" was renamed from OLD to DB between
    // up and down; the resource itself (by name) is still the same one.
    const lockService = { resources: { d1: [{ binding: "OLD", name: "n-api-db" }] } };
    const configService = { key: "api", d1: [{ binding: "DB" }] };
    const result = mergeResources(lockService, configService, "n");
    expect(result.d1).toHaveLength(1);
  });

  it("treats an undefined lock service the same as one with no resources", () => {
    const configService = { key: "api", kv: [{ binding: "CACHE" }] };
    const result = mergeResources(undefined, configService, "n");
    expect(result.kv).toEqual([{ binding: "CACHE", name: "n-api-cache" }]);
  });

  it("treats an undefined config service the same as one declaring nothing", () => {
    const lockService = { resources: { r2: [{ binding: "ASSETS", name: "n-api-assets" }] } };
    const result = mergeResources(lockService, undefined, "n");
    expect(result.r2).toEqual([{ binding: "ASSETS", name: "n-api-assets" }]);
    expect(result.d1).toEqual([]);
    expect(result.kv).toEqual([]);
    expect(result.queues).toEqual([]);
  });

  it("merges each resource type independently", () => {
    const lockService = {
      resources: {
        d1: [{ binding: "DB", name: "n-api-db" }],
        queues: [{ binding: "JOBS", name: "n-api-jobs" }],
      },
    };
    const configService = { key: "api", kv: [{ binding: "CACHE" }] };
    const result = mergeResources(lockService, configService, "n");
    expect(result.d1).toEqual([{ binding: "DB", name: "n-api-db" }]);
    expect(result.kv).toEqual([{ binding: "CACHE", name: "n-api-cache" }]);
    expect(result.r2).toEqual([]);
    expect(result.queues).toEqual([{ binding: "JOBS", name: "n-api-jobs" }]);
  });
});
