#!/usr/bin/env python3
"""
W3.4 QUESTION (3), the credential-free half: does the document `linear_api` actually sends still
VALIDATE against Linear's live schema?

WHY THIS IS ANSWERABLE WITHOUT A TENANT. Linear validates a GraphQL document BEFORE it
authenticates — the same property a prior session used to pin `Issue.dueDate` and
`Issue.completedAt`'s scalar types. So an unauthenticated POST separates two outcomes that matter:

    document VALID   → the server gets as far as auth and answers 401 (no validation errors)
    document INVALID → the server answers with GraphQL validation errors and never reaches auth

WHY IT MATTERS. internal/importer/linear.go says it in its own comment: "an unknown field 400s the
WHOLE query". `linearIssuesQuery` selects 11 fields across 4 types in one document, so a single
renamed or removed field on Linear's side takes out EVERY linear_api import — not one column.
Nothing in this repo has ever asked whether that document still validates; the package's Linear
tests all drive a local stub that answers whatever the test wrote.

⚠ THE DOCUMENT IS READ OUT OF THE SOURCE, NEVER RETYPED. A probe that validates a query I typed
into the probe measures the probe. `linearIssuesQuery` is extracted from the backtick literal in
internal/importer/linear.go and its sha256 is printed, so the bytes under test are the bytes the
product sends.

THE CONTROLS ARE THE POINT, because "401" is exactly what a probe that never sent anything
meaningful would also produce:

  N1  a FABRICATED field on Issue          must be REJECTED (validation), never 401
  N2  a syntactically malformed document   must be REJECTED (parse), never 401
  N3  a fabricated ROOT field              must be REJECTED, never 401
  N4  the trivial valid document {__typename}  must reach auth (401) — the must-stay-green that
      proves REJECTED is not simply what this endpoint says to everything
  N5  a fabricated ARGUMENT on team(...)   must be REJECTED — argument names are validated too,
      which is what makes the `team(id:)` signature part of the claim rather than an assumption

A run in which N1/N2/N3/N5 are not all REJECTED, or N4 is not 401, says the instrument is blind
and the verdict on the real document means nothing.

Usage:  python3 scripts/w34-linear-api-document-validity-probe.py
"""

import hashlib
import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

REPO = pathlib.Path(__file__).resolve().parent.parent
SRC = REPO / "internal" / "importer" / "linear.go"
URL_CONST = re.compile(r'const defaultLinearURL = "([^"]+)"')
QUERY_CONST = re.compile(r"const linearIssuesQuery = `([^`]*)`", re.S)


def from_source():
    """Return (url, query) taken from the product source, or die loudly."""
    s = SRC.read_text()
    m = QUERY_CONST.search(s)
    if not m:
        sys.exit(
            "ANCHOR MISS: could not find `const linearIssuesQuery = ` ... ` in "
            f"{SRC}. The probe would otherwise validate a document I typed myself."
        )
    u = URL_CONST.search(s)
    if not u:
        sys.exit(f"ANCHOR MISS: could not find defaultLinearURL in {SRC}.")
    return u.group(1), m.group(1)


def post(url, query, variables=None):
    """Return (http_status, parsed_body_or_raw)."""
    payload = {"query": query}
    if variables is not None:
        payload["variables"] = variables
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=25) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except json.JSONDecodeError:
            return e.code, raw


def classify(status, body):
    """VALID-REACHED-AUTH | REJECTED | UNKNOWN, plus the messages seen.

    A GraphQL validation/parse failure carries errors[] whose extensions.code (or message text)
    names the document as the problem. An auth failure also carries errors[], so the CODE is what
    separates them — never the status, which is 400 for both on this endpoint.
    """
    msgs, codes = [], []
    if isinstance(body, dict):
        for e in body.get("errors", []) or []:
            msgs.append(str(e.get("message", ""))[:160])
            ext = e.get("extensions") or {}
            codes.append(str(ext.get("code") or ext.get("type") or ""))
    blob = " ".join(msgs + codes).lower()
    doc_words = (
        "cannot query field",
        "unknown argument",
        "syntax error",
        "graphql_validation_failed",
        "graphql_parse_failed",
        "bad_user_input",
        "validation",
        "did you mean",
    )
    auth_words = ("authentication", "authenticate", "unauthorized", "api key", "authentication_error")
    if any(w in blob for w in doc_words):
        return "REJECTED", msgs
    if status == 401 or any(w in blob for w in auth_words):
        return "VALID-REACHED-AUTH", msgs
    return "UNKNOWN", msgs or [str(body)[:200]]


def main():
    url, query = from_source()
    digest = hashlib.sha256(query.encode()).hexdigest()[:16]
    print("=" * 96)
    print("W3.4 QUESTION (3) — does the document linear_api sends still validate?")
    print("=" * 96)
    print(f"endpoint (from source): {url}")
    print(f"linearIssuesQuery sha256[:16] = {digest}  ({len(query)} bytes, read from {SRC.name})")
    print()

    variables = {"teamId": "00000000-0000-0000-0000-000000000000", "after": None}

    controls = [
        ("N1  fabricated FIELD on Issue", query.replace("dueDate", "dueDateZZZ", 1), "REJECTED"),
        ("N2  malformed document", "query { team(id: ", "REJECTED"),
        ("N3  fabricated ROOT field", "query { zzzNoSuchRootField { id } }", "REJECTED"),
        ("N4  trivial valid document", "{ __typename }", "VALID-REACHED-AUTH"),
        (
            "N5  fabricated ARGUMENT on team()",
            query.replace("team(id: $teamId)", "team(id: $teamId, zzzNope: 1)", 1),
            "REJECTED",
        ),
    ]

    ok = True
    for name, doc, want in controls:
        st, body = post(url, doc, variables)
        got, msgs = classify(st, body)
        mark = "OK " if got == want else "!! "
        if got != want:
            ok = False
        print(f"{mark}{name}: HTTP {st} → {got} (want {want})")
        if msgs:
            print(f"      {msgs[0]}")
    print("-" * 96)
    if not ok:
        print("!! THE INSTRUMENT IS BLIND — a control did not behave as required.")
        print("   The verdict on the real document below means nothing. Do not record it.")
        sys.exit(2)
    print("all 5 controls behaved: this endpoint validates the document before it authenticates,")
    print("and this probe can tell a rejected document from one that reached auth.")
    print()

    st, body = post(url, query, variables)
    got, msgs = classify(st, body)
    print("=" * 96)
    print(f"THE REAL DOCUMENT: HTTP {st} → {got}")
    for m in msgs:
        print(f"   {m}")
    print("=" * 96)
    if got == "VALID-REACHED-AUTH":
        print("VERDICT: the document linear_api sends still validates against Linear's live schema.")
        print("         This says NOTHING about whether an import has ever run end to end — only")
        print("         that it cannot be failing on document validity. QUESTION (3)'s remaining")
        print("         half needs a tenant.")
    elif got == "REJECTED":
        print("VERDICT: THE DOCUMENT DOES NOT VALIDATE. Every linear_api import fails at the first")
        print("         page, for every tenant, regardless of credentials — a structural zero.")
    else:
        print("VERDICT: UNKNOWN — do not record a result from this run.")
        sys.exit(3)


if __name__ == "__main__":
    main()
