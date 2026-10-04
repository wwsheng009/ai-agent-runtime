import { describe, expect, it } from "vitest";

import {
  parseKeyValueEntries,
  serializeKeyValueEntries,
} from "./key-value-rows-model";

describe("parseKeyValueEntries", () => {
  it("reads string values and stringifies non-strings", () => {
    expect(parseKeyValueEntries('{"a":"1","b":2,"c":null}')).toEqual([
      { key: "a", value: "1" },
      { key: "b", value: "2" },
      { key: "c", value: "" },
    ]);
  });

  it("returns empty for empty, invalid, or non-object json", () => {
    expect(parseKeyValueEntries("{}")).toEqual([]);
    expect(parseKeyValueEntries("")).toEqual([]);
    expect(parseKeyValueEntries("{oops")).toEqual([]);
    expect(parseKeyValueEntries("[1,2]")).toEqual([]);
    expect(parseKeyValueEntries("null")).toEqual([]);
  });
});

describe("serializeKeyValueEntries", () => {
  it("drops empty keys, trims names, keeps insertion order", () => {
    const json = serializeKeyValueEntries([
      { key: " X-Trace ", value: "abc" },
      { key: "", value: "ignored" },
      { key: "X-Org", value: "42" },
    ]);
    expect(JSON.parse(json)).toEqual({ "X-Trace": "abc", "X-Org": "42" });
    expect(Object.keys(JSON.parse(json))).toEqual(["X-Trace", "X-Org"]);
  });

  it("round-trips a parsed record", () => {
    const json = '{"*":"target","gpt-4o":"gpt-4o-mini"}';
    expect(serializeKeyValueEntries(parseKeyValueEntries(json))).toBe(
      JSON.stringify({ "*": "target", "gpt-4o": "gpt-4o-mini" }, null, 2),
    );
  });
});
