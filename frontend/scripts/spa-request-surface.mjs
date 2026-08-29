// spa-request-surface.mjs — what every request the SPA issues actually puts on the wire:
// verb, path, the QUERY keys it attaches, and the BODY fields it can send.
//
// ⚠ WHY THE COMPILER AND NOT A SCAN. Two regex censuses of this same question were built and
// thrown away (W3.69 records both). A window bounded by a character count reads the next
// function's body; a window bounded by the next `/v1` literal still mis-attributes, because a
// TypeScript PARAMETER TYPE ANNOTATION spelled `body: { ... }` sits BEFORE its function's request
// literal. And no scan resolves `Partial<T>` or a `...spread`, which is exactly where the one
// defect this repository has actually shipped in this class was hiding (W3.68: the api module's
// body was clean and the HOOK spread a retired field in).
//
// The type checker answers all three by construction: getTypeAtLocation(body).getProperties() is
// the set of keys that expression can carry, whatever syntax produced it.
//
// Emits JSON on stdout. Field semantics, stated because they are not interchangeable:
//   bodyFields   — the UPPER BOUND of keys this call site can send. For `body: Partial<Issue>`
//                  that is every Issue property, not the ones some caller happens to pass. Upper
//                  bound is the safe direction: if every possible key is accepted, no caller 400s.
//   bodyUnbounded— true when the body type is `unknown`/`any`/an index signature, i.e. the checker
//                  cannot bound it. NOT the same as an empty set, and must never be read as one.
//   queryFields  — keys handed to qs(). An `as Record<...>` assertion erases them, so the
//                  expression is unwrapped before the type is taken.
import ts from "typescript";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..");

const cfgPath = ts.findConfigFile(root, ts.sys.fileExists, "tsconfig.json");
if (!cfgPath) throw new Error("tsconfig.json not found under " + root);
const cfg = ts.parseJsonConfigFileContent(
  ts.readConfigFile(cfgPath, ts.sys.readFile).config,
  ts.sys,
  path.dirname(cfgPath),
);
const program = ts.createProgram(cfg.fileNames, cfg.options);
const checker = program.getTypeChecker();

const REQUEST_FNS = new Set(["apiRequest", "publicRequest"]);

// unwrap `x as T` / `<T>x` / `x!` so the checker sees the original expression's type
function unwrap(node) {
  while (
    ts.isAsExpression(node) ||
    ts.isTypeAssertionExpression(node) ||
    ts.isNonNullExpression(node) ||
    ts.isParenthesizedExpression(node)
  ) {
    node = node.expression;
  }
  return node;
}

// objectish reports whether a type has properties of its own (an object, or an array/union of
// them) — i.e. whether a DEPTH-1 census stops short of the whole answer for that property.
// Go's DisallowUnknownFields applies RECURSIVELY, so a nested field is as fatal as a top-level
// one; this is what lets the residual be counted instead of hand-waved.
function objectish(t) {
  const PRIMITIVE =
    ts.TypeFlags.Any | ts.TypeFlags.Unknown | ts.TypeFlags.StringLike |
    ts.TypeFlags.NumberLike | ts.TypeFlags.BooleanLike | ts.TypeFlags.BigIntLike |
    ts.TypeFlags.ESSymbolLike | ts.TypeFlags.EnumLike | ts.TypeFlags.VoidLike |
    ts.TypeFlags.Null | ts.TypeFlags.Undefined | ts.TypeFlags.Never;
  const parts = t.isUnion() ? t.types : [t];
  for (const p of parts) {
    const f = p.getFlags();
    // ⚠ THE PRIMITIVE FILTER IS LOAD-BEARING AND ITS ABSENCE WAS CAUGHT BY READING THE OUTPUT:
    // getPropertiesOfType(string) returns String.prototype (length, charAt, …), so without this
    // every `email: string` was reported as a nested object and the residual read as 21 of 22
    // sites — a number so obviously wrong it was checkable, which is the only reason it was caught.
    if (f & PRIMITIVE) continue;
    if (!(f & ts.TypeFlags.Object)) continue;
    const num = checker.getIndexInfoOfType(p, ts.IndexKind.Number);
    if (num) {
      if (objectish(num.type)) return true;
      continue;
    }
    if (checker.getPropertiesOfType(p).length > 0) return true;
  }
  return false;
}

function propNames(node) {
  const t = checker.getTypeAtLocation(unwrap(node));
  const flags = t.getFlags();
  if (flags & (ts.TypeFlags.Any | ts.TypeFlags.Unknown)) return { unbounded: true, names: [] };
  const names = new Set();
  let unbounded = false;
  // a union (e.g. `A | undefined`) contributes every constituent's properties
  const parts = t.isUnion() ? t.types : [t];
  for (const p of parts) {
    if (p.getFlags() & (ts.TypeFlags.Any | ts.TypeFlags.Unknown)) { unbounded = true; continue; }
    if (checker.getIndexInfoOfType(p, ts.IndexKind.String)) unbounded = true;
    for (const sym of checker.getPropertiesOfType(p)) names.add(sym.getName());
  }
  const nested = new Set();
  for (const p of parts) {
    if (p.getFlags() & (ts.TypeFlags.Any | ts.TypeFlags.Unknown)) continue;
    for (const sym of checker.getPropertiesOfType(p)) {
      const pt = checker.getTypeOfSymbolAtLocation(sym, unwrap(node));
      if (objectish(pt)) nested.add(sym.getName());
    }
  }
  return { unbounded, names: [...names].sort(), nested: [...nested].sort() };
}

// pathOf renders a template literal with every `${...}` span as `{}`, and pulls the qs() argument
// out of a `${qs({...})}` span so the query keys can be typed separately.
function pathOf(node) {
  let qsArg = null;
  const render = (n) => {
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) return n.text;
    if (!ts.isTemplateExpression(n)) return null;
    let out = n.head.text;
    for (const span of n.templateSpans) {
      const e = span.expression;
      if (ts.isCallExpression(e) && e.expression.getText() === "qs" && e.arguments.length) {
        qsArg = e.arguments[0];
      } else {
        out += "{}";
      }
      out += span.literal.text;
    }
    return out;
  };
  return { text: render(node), qsArg };
}

const sites = [];
for (const sf of program.getSourceFiles()) {
  if (sf.isDeclarationFile) continue;
  const rel = path.relative(root, sf.fileName);
  if (!rel.startsWith("src" + path.sep)) continue;
  ts.forEachChild(sf, function walk(node) {
    if (ts.isCallExpression(node)) {
      const name = ts.isIdentifier(node.expression)
        ? node.expression.text
        : ts.isPropertyAccessExpression(node.expression)
          ? node.expression.name.text
          : "";
      if (REQUEST_FNS.has(name) && node.arguments.length >= 1) {
        const { text, qsArg } = pathOf(node.arguments[0]);
        if (text && text.includes("/v1")) {
          let verb = "GET";
          let body = null;
          let spreadOpts = false;
          const opts = node.arguments[1];
          if (opts && ts.isObjectLiteralExpression(opts)) {
            for (const p of opts.properties) {
              // ⚠ SHORTHAND IS THE MAJORITY SPELLING AND THE FIRST VERSION OF THIS SCRIPT DID NOT
              // HANDLE IT: `{ method: "POST", body }` is a ShorthandPropertyAssignment, not a
              // PropertyAssignment, so a check for the latter alone saw 10 bodies where there are
              // 29 — a 3x undercount, silently, in the flattering direction.
              let key = null;
              let val = null;
              if (ts.isPropertyAssignment(p) && p.name) {
                key = p.name.getText();
                val = p.initializer;
              } else if (ts.isShorthandPropertyAssignment(p)) {
                key = p.name.getText();
                val = p.name;
              } else if (ts.isSpreadAssignment(p)) {
                // a spread into the options object can carry `body` from anywhere; refuse to
                // guess rather than report a body-less call site
                spreadOpts = true;
                continue;
              } else {
                continue;
              }
              if (key === "method") {
                const v = unwrap(val);
                if (ts.isStringLiteral(v)) verb = v.text.toUpperCase();
                else verb = "?" + v.getText();
              } else if (key === "body") {
                body = val;
              }
            }
          }
          const q = qsArg ? propNames(qsArg) : { unbounded: false, names: [], nested: [] };
          const b = body ? propNames(body) : null;
          sites.push({
            file: rel,
            line: sf.getLineAndCharacterOfPosition(node.getStart()).line + 1,
            verb,
            path: text.slice(text.indexOf("/v1")),
            queryFields: q.names,
            queryUnbounded: q.unbounded,
            hasBody: body !== null,
            optionsSpread: spreadOpts,
            bodyFields: b ? b.names : [],
            bodyUnbounded: b ? b.unbounded : false,
            bodyNestedFields: b ? b.nested : [],
          });
        }
      }
    }
    ts.forEachChild(node, walk);
  });
}

sites.sort((a, b) =>
  (a.verb + a.path + a.file + a.line).localeCompare(b.verb + b.path + b.file + b.line));
process.stdout.write(JSON.stringify({ sites }, null, 2) + "\n");
