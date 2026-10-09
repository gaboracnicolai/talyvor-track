import { describe, expect, it } from "vitest";
import { unignored } from "./npm-audit.mjs";

const advisory = (name, ghsa) => ({
  name,
  severity: "high",
  via: [{ name, url: `https://github.com/advisories/${ghsa}`, range: "<=1.0.0", severity: "high", title: "t" }],
});

describe("npm-audit gate", () => {
  it("reports a known-vulnerable dependency unless its advisory is ignored", () => {
    const report = {
      vulnerabilities: {
        braces: advisory("braces", "GHSA-vfj7-8cjw-p6xm"),
        tinypool: advisory("tinypool", "GHSA-5gmw-xhrv-c9v3"),
        chokidar: { name: "chokidar", via: ["braces"] },
      },
    };
    const left = unignored(report, { "GHSA-vfj7-8cjw-p6xm": "no fix" });
    expect(left.map(([ghsa]) => ghsa)).toEqual(["GHSA-5gmw-xhrv-c9v3"]);
  });

  it("fails closed when npm audit returns no report", () => {
    expect(() => unignored({ error: { code: "ENOLOCK" } }, {})).toThrow(/no report/);
  });
});
