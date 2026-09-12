import { describe, expect, it } from "vitest";
import { parseArgs } from "../src/cli-args.mjs";

describe("parseArgs", () => {
  it("defaults to help with no arguments", () => {
    expect(parseArgs([])).toEqual({ command: "help", name: undefined, output: undefined, noOpen: false });
  });

  it.each([["help"], ["--help"], ["-h"]])("recognizes %s as help", (arg) => {
    expect(parseArgs([arg])).toEqual({ command: "help", name: undefined, output: undefined, noOpen: false });
  });

  it("parses a bare `up` with no name or flags", () => {
    expect(parseArgs(["up"])).toEqual({ command: "up", name: undefined, output: undefined, noOpen: false });
  });

  it("parses `up` with a name", () => {
    expect(parseArgs(["up", "blue-honey-badger-12345"])).toEqual({
      command: "up",
      name: "blue-honey-badger-12345",
      output: undefined,
      noOpen: false,
    });
  });

  it("parses `up --output <path>`", () => {
    expect(parseArgs(["up", "--output", "out.json"])).toEqual({
      command: "up",
      name: undefined,
      output: "out.json",
      noOpen: false,
    });
  });

  it("parses `up --no-open`", () => {
    expect(parseArgs(["up", "--no-open"])).toEqual({
      command: "up",
      name: undefined,
      output: undefined,
      noOpen: true,
    });
  });

  it("parses a name plus both flags, in any order", () => {
    expect(parseArgs(["up", "--no-open", "my-name-here-00001", "--output", "out.json"])).toEqual({
      command: "up",
      name: "my-name-here-00001",
      output: "out.json",
      noOpen: true,
    });
  });

  it("parses `down <name>`", () => {
    expect(parseArgs(["down", "blue-honey-badger-12345"])).toEqual({
      command: "down",
      name: "blue-honey-badger-12345",
      output: undefined,
      noOpen: false,
    });
  });

  it("throws on an unknown command", () => {
    expect(() => parseArgs(["destroy"])).toThrow(/Unknown command "destroy"/);
  });

  it("throws when --output is given no value", () => {
    expect(() => parseArgs(["up", "--output"])).toThrow(/"--output" requires a value/);
  });

  it("throws on an unknown flag", () => {
    expect(() => parseArgs(["up", "--bogus"])).toThrow(/Unknown option "--bogus"/);
  });

  it.each([["--output", ["down", "name", "--output", "x"]], ["--no-open", ["down", "name", "--no-open"]]])(
    "treats %s as an unknown option on `down`",
    (flag, args) => {
      expect(() => parseArgs(args)).toThrow(new RegExp(`Unknown option "${flag.replace(/[-]/g, "\\-")}"`));
    },
  );

  it("throws on two positionals", () => {
    expect(() => parseArgs(["up", "first", "second"])).toThrow(/Unexpected argument "second"/);
  });
});
