#!/usr/bin/env python3
"""w34-resolvedws-controls-8j5q.py — positive controls for the N1 structural guard.

WHAT IS UNDER TEST
  internal/authz/resolved_workspace_test.go —
  TestAuthorizeWorkspace_DownstreamUsesTheResolvedWorkspace.

WHY A HARNESS AT ALL. The guard's subject is a property NO behavioural test can hold:
AuthorizeWorkspace matches on exact string equality, so the caller-supplied workspace id
and the resolved Membership.WorkspaceID are the same string whenever the gate passes.
A rule about a distinction nothing can observe is exactly the kind of rule that can be
silently inert, so every claim it makes is mutated here and scored.

PROTOCOL
  · Every control declares its PREDICTED verdict BEFORE it runs.
  · Every control is restored in a `finally` and the restoration is sha256-verified.
  · CAUGHT means the guard FAILED (exit != 0) AND, where a site is named, its message
    names the mutated file — a red for the wrong reason is not a catch.
"""

import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

GUARD = ["go", "test", "-count=1", "-run",
         "TestAuthorizeWorkspace_DownstreamUsesTheResolvedWorkspace", "./internal/authz/"]
# #174's behavioural cross-tenant test — the complementary instrument. Needs real Postgres.
BEHAVIOURAL = ["go", "test", "-count=1", "-run", "CrossTenant", "./internal/integrations/"]

HUB = "internal/realtime/hub.go"
IMPORTER = "internal/importer/handler.go"
INTEGRATIONS = "internal/integrations/handler.go"
MCP = "internal/mcp/server.go"
GUARD_SRC = "internal/authz/resolved_workspace_test.go"

HUB_GATE = """	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok || m.MemberID == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}"""
HUB_NEWCLIENT = "client := newClient(uuid.NewString(), m.WorkspaceID, memberID, conn)"

INTEGRATIONS_STATUS_GATE = """	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	provider := chi.URLParam(r, "provider")"""


def sha(path):
    with open(os.path.join(REPO, path), "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read(path):
    with open(os.path.join(REPO, path), encoding="utf-8") as fh:
        return fh.read()


def write(path, text):
    with open(os.path.join(REPO, path), "w", encoding="utf-8") as fh:
        fh.write(text)


def run(cmd):
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr)


class Control:
    def __init__(self, name, predicted, edits, expect_file=None, note="", cmd=None):
        self.name, self.predicted, self.edits = name, predicted, edits
        self.expect_file, self.note = expect_file, note
        # Which instrument this control interrogates. Defaults to the structural guard;
        # C9b points it at the BEHAVIOURAL test so the division of labour C9 CLAIMS is
        # measured rather than asserted. (Added by tab-8x2m: BEHAVIOURAL was defined and
        # never run, so C9's "that is the behavioural test's job" rested on nothing.)
        self.cmd = cmd or GUARD


def apply_edits(edits):
    """edits: list of (path, old, new). Applied against the CURRENT text of each file,
    re-read between edits, so two edits to one file cannot erase each other."""
    for path, old, new in edits:
        text = read(path)
        if text.count(old) != 1:
            raise SystemExit(f"ANCHOR DEAD in {path}: {text.count(old)} occurrences, expected 1\n"
                             f"--- anchor ---\n{old}\n")
        write(path, text.replace(old, new))


CONTROLS = [
    Control(
        "C1 the defect itself — the socket registered with the RAW query workspace",
        "CAUGHT", [(HUB, HUB_NEWCLIENT,
                    "client := newClient(uuid.NewString(), workspaceID, memberID, conn)")],
        expect_file="realtime/hub.go",
        note="this is what main shipped until this merge"),
    Control(
        "C2 importer upload passes the raw arg to the import fn",
        "CAUGHT", [(IMPORTER, "out, err := fn(r.Context(), m.WorkspaceID, teamID, file)",
                    "out, err := fn(r.Context(), workspaceID, teamID, file)")],
        expect_file="importer/handler.go"),
    Control(
        "C3 integrations status store call takes the raw arg — N1 VERBATIM",
        "CAUGHT", [(INTEGRATIONS, "in, err := h.store.Get(r.Context(), m.WorkspaceID, provider)",
                    "in, err := h.store.Get(r.Context(), workspaceID, provider)")],
        expect_file="integrations/handler.go",
        note="the exact mutation #174 measured as CAUGHT BY NOTHING"),
    Control(
        "C4 mcp stamps the context with the raw tool argument",
        "CAUGHT", [(MCP, "ctx = authz.WithAuthorized(ctx, m.WorkspaceID, m.MemberID)",
                    "ctx = authz.WithAuthorized(ctx, ws, m.MemberID)")],
        expect_file="mcp/server.go"),
    Control(
        "C5 LAUNDERING: reassign workspaceID = m.WorkspaceID, then keep using the name",
        "CAUGHT", [(HUB, "	memberID := m.MemberID",
                    "	memberID := m.MemberID\n	workspaceID = m.WorkspaceID")],
        expect_file="realtime/hub.go",
        note="the near-miss: a rule keyed on the ARGUMENT NAME must still fire here"),
    Control(
        "C6 DISCARD EVASION: throw the Membership away and keep the caller's string",
        "CAUGHT", [(HUB, HUB_GATE,
                    """	_, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}"""),
                   (HUB, "	memberID := m.MemberID", "	memberID := workspaceID"),
                   (HUB, HUB_NEWCLIENT,
                    "client := newClient(uuid.NewString(), workspaceID, memberID, conn)")],
        expect_file="realtime/hub.go",
        note="proves the rule is not keyed on the Membership being bound"),
    Control(
        "C7 BLIND THE SCAN BY CALLEE NAME, with C1's defect on top",
        "NOT CAUGHT BY THE RULE — THE FLOOR REDS INSTEAD",
        [(HUB, HUB_NEWCLIENT,
          "client := newClient(uuid.NewString(), workspaceID, memberID, conn)"),
         (GUARD_SRC, 'scanGateSites(t, root, "AuthorizeWorkspace")',
          'scanGateSites(t, root, "AuthorizeWorkspaceNOPE")')],
        expect_file="floor",
        note="the measured blindness: a rule matching nothing must not report a clean product"),
    Control(
        "C8 BLIND THE WALK (drop internal/ from the scanned roots), with C1 on top",
        "NOT CAUGHT BY THE RULE — THE PARSED-FILES FLOOR REDS INSTEAD",
        [(HUB, HUB_NEWCLIENT,
          "client := newClient(uuid.NewString(), workspaceID, memberID, conn)"),
         (GUARD_SRC, 'for _, sub := range []string{"internal", "cmd"} {',
          'for _, sub := range []string{"cmd"} {')],
        expect_file="floor",
        note="a walk that stops reading the tree is the other way to be silently inert"),
    Control(
        "C9 MUST STAY GREEN: delete the integrations status REFUSAL, leave the call",
        "NOT CAUGHT", [(INTEGRATIONS, INTEGRATIONS_STATUS_GATE,
                        """	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok && false {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	provider := chi.URLParam(r, "provider")""")],
        note="the structural guard says NOTHING about the refusal — that is #174's "
             "behavioural test's job, and this control keeps CAUGHT from being a catch-all"),
    Control(
        "C9b THE OTHER HALF OF C9, MEASURED: same refusal deleted, asked of the BEHAVIOURAL test",
        "CAUGHT", [(INTEGRATIONS, INTEGRATIONS_STATUS_GATE,
                    """	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok && false {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	provider := chi.URLParam(r, "provider")""")],
        cmd=BEHAVIOURAL,
        expect_file="handler_test.go",
        note="C9 says the refusal is HELD ELSEWHERE. Nothing checked that. If this is NOT "
             "CAUGHT then C9's green is a hole, not a division of labour. Needs real Postgres."),
    Control(
        "C10 PRE-GATE ALIAS: copy the caller's string to another name BEFORE the gate, use the copy",
        "NOT CAUGHT — AN UNDOCUMENTED LIMIT OF THE RULE, MEASURED HERE RATHER THAN ASSUMED",
        [(HUB, HUB_GATE, "\twsID := workspaceID\n" + HUB_GATE),
         (HUB, HUB_NEWCLIENT,
          "client := newClient(uuid.NewString(), wsID, memberID, conn)")],
        note="the rule tracks the ARGUMENT NAME. An alias taken before the call carries the "
             "same caller-supplied string under a name the rule never learned. Scored so the "
             "limit is written down from a measurement instead of being discovered later."),
]


def main():
    files = sorted({p for c in CONTROLS for (p, _, _) in c.edits})
    snapshot = {p: read(p) for p in files}
    baseline = {p: sha(p) for p in files}

    for label, cmd in (("U0 structural guard", GUARD), ("U0b behavioural cross-tenant", BEHAVIOURAL)):
        code, out = run(cmd)
        print(f"{label}, pristine tree -> {'GREEN' if code == 0 else 'RED'}")
        if code != 0:
            print(out)
            raise SystemExit(f"{label} baseline is not green — fix that before scoring any control")

    results = []
    for c in CONTROLS:
        print(f"\n=== {c.name}\n    PREDICTED: {c.predicted}"
              + (f"\n    note: {c.note}" if c.note else ""))
        try:
            apply_edits(c.edits)
            code, out = run(c.cmd)
            caught = code != 0
            named = (c.expect_file in out) if (c.expect_file and c.expect_file != "floor") else None
            floor = ("floor is" in out)
            if c.expect_file == "floor":
                verdict = "FLOOR RED" if (caught and floor) else ("RULE RED" if caught else "GREEN")
            elif caught and named is not False:
                verdict = "CAUGHT"
            elif caught:
                verdict = "RED BUT DID NOT NAME " + str(c.expect_file)
            else:
                verdict = "NOT CAUGHT"
            # A rule-red must NOT be a floor-red in disguise.
            if verdict == "CAUGHT" and floor:
                verdict = "CAUGHT VIA FLOOR — NOT THE RULE"
            print(f"    ACTUAL:    {verdict}")
            if verdict.startswith("CAUGHT") or verdict == "FLOOR RED":
                for line in out.splitlines():
                    if "hub.go" in line or "handler.go" in line or "server.go" in line or "floor is" in line:
                        print("      | " + line.strip()[:160])
            results.append((c.name, c.predicted, verdict))
        finally:
            # Restore from the in-memory snapshot, NOT from git: the guard and this
            # harness are untracked on a fresh branch, and `git checkout --` fails on
            # an untracked pathspec — which aborts the WHOLE restore and leaves the
            # tree mutated. (Measured: it did exactly that on the first run.)
            for p in files:
                write(p, snapshot[p])
                assert sha(p) == baseline[p], f"RESTORE FAILED for {p}"

    code, out = run(GUARD)
    print(f"\nU0' after all restores -> {'GREEN' if code == 0 else 'RED'}")
    for p in files:
        assert sha(p) == baseline[p]
    print("all files sha256-identical to baseline")

    print("\n" + "=" * 78)
    ok = True
    for name, pred, actual in results:
        agree = (pred.split(" —")[0].strip().startswith(actual.split(" —")[0].strip())
                 or actual.startswith(pred.split(" —")[0].strip())
                 or (pred.startswith("NOT CAUGHT BY THE RULE") and actual == "FLOOR RED"))
        ok &= agree
        print(f"{'OK ' if agree else 'XX '} {name}\n     predicted={pred}\n     actual   ={actual}")
    print("=" * 78)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the rows marked XX")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
