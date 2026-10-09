// npm-audit.mjs — B28.455: fail CI on any known-vulnerable frontend dependency.
//
// npm has no ignore list (suite's pnpm has auditConfig.ignoreGhsas), so this runs `npm audit --json`
// over package-lock.json and fails on every advisory not named in package.json "auditIgnore", whose
// value for each GHSA is the reason it is ignored. An audit that returns no report fails too.
import { spawnSync } from "node:child_process";
import { readFileSync, realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Advisories in an `npm audit --json` report that `ignore` (GHSA id → reason) does not name.
export function unignored(report, ignore) {
  if (!report.vulnerabilities) throw new Error(`npm audit gave no report: ${JSON.stringify(report).slice(0, 500)}`);
  const found = new Map();
  for (const v of Object.values(report.vulnerabilities))
    for (const via of v.via)
      if (typeof via === "object") found.set(via.url.split("/").pop(), `${via.name} ${via.range} (${via.severity}): ${via.title}`);
  return [...found].filter(([ghsa]) => !Object.hasOwn(ignore, ghsa));
}

// realpath: import.meta.url is symlink-resolved and argv[1] is not; a mismatch here would skip the gate and exit 0.
if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const root = new URL("..", import.meta.url);
  const { auditIgnore = {} } = JSON.parse(readFileSync(new URL("package.json", root), "utf8"));
  const run = spawnSync("npm", ["audit", "--json"], { cwd: root, encoding: "utf8", maxBuffer: 1 << 26 });
  const left = unignored(JSON.parse(run.stdout || "{}"), auditIgnore);
  for (const [ghsa, reason] of Object.entries(auditIgnore)) console.log(`ignored ${ghsa}: ${reason}`);
  for (const [ghsa, what] of left) console.log(`::error::${ghsa} ${what}`);
  console.log(left.length ? `${left.length} known-vulnerable advisories` : "npm audit: clean");
  process.exit(left.length ? 1 : 0);
}
