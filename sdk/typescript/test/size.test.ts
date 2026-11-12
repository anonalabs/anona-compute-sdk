import { describe, expect, it } from "vitest";

import { SIZE_METADATA_KEY, parseSize, withSize } from "../src/index.js";

describe("sandbox sizes", () => {
  it("normalises size names", () => {
    expect(parseSize("L")).toBe("large");
    expect(parseSize("xlarge")).toBe("xl");
    expect(() => parseSize("huge")).toThrow(/small, medium, large, xl/);
  });

  it("puts the size in create metadata, keeping the rest", () => {
    const opts = withSize("medium", { timeoutMs: 60_000, metadata: { name: "d" } });
    expect(opts).toEqual({ timeoutMs: 60_000, metadata: { name: "d", [SIZE_METADATA_KEY]: "medium" } });
    expect(withSize("small")).toEqual({ metadata: { [SIZE_METADATA_KEY]: "small" } });
  });

  it("accepts create options without metadata (compiles and keeps them)", () => {
    const opts = withSize("large", { timeoutMs: 300_000 });
    expect(opts).toEqual({ timeoutMs: 300_000, metadata: { [SIZE_METADATA_KEY]: "large" } });
  });
});
