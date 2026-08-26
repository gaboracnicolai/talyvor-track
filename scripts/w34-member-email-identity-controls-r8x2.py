#!/usr/bin/env python3
"""Controls for email_identity_measured_test.go (W3.4, tab-r8x2).

⚠ THIS FILE'S TESTS PIN A HAZARD, NOT A FIX, SO THE CONTROL QUESTION IS INVERTED. They pass on
today's tree by design. What has to be proved is that each one goes RED the moment the thing it
measures CHANGES -- otherwise they are three tests that would keep passing through the very fix
they exist to notice, and the queue would carry a "measured" claim nothing was watching.

Each arm therefore applies a plausible REAL FIX (or a real no-op) to production code and checks the
tests react as their own failure messages promise.

  new = the three TestMeasured_ tests in internal/member/
  old = internal/member/ + internal/authz/ + internal/guest/ with this file moved away -- i.e.
        whether the pre-existing suite has anything to say about email identity at all.

REFUSALS: a dirty tree, a missing TRACK_TEST_DATABASE_URL (every arm would score CAUGHT), a
mutation that changed no bytes, or a post-run sha256 that does not match.
"""

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MGMT = os.path.join(REPO, "internal/member/mgmt.go")
RESOLVER = os.path.join(REPO, "internal/authz/resolver.go")
GUEST = os.path.join(REPO, "internal/guest/store.go")
NEWTEST = os.path.join(REPO, "internal/member/email_identity_measured_test.go")
NEWTESTS = "TestMeasured_"
PKGS = ["./internal/member/", "./internal/authz/", "./internal/guest/"]

# THE FIX, port 1: AddMember applies the rule guest/store.go:259 already applies.
MGMT_NORMALISE = ("\t\tworkspaceID, email, role))\n",
                  "\t\tworkspaceID, strings.ToLower(strings.TrimSpace(email)), role))\n")
MGMT_IMPORT = ('\t"errors"\n\t"fmt"\n', '\t"errors"\n\t"fmt"\n\t"strings"\n')
# THE FIX, port 2: the resolver stops keying on byte equality.
RESOLVER_CI = ("`SELECT workspace_id, id, role FROM members WHERE email = $1`, email)",
               "`SELECT workspace_id, id, role FROM members WHERE LOWER(email) = LOWER($1)`, email)")
# VOID: the guest store's normalisation wrapped in itself. A real edit, arithmetically identity.
GUEST_VOID = ("\t\tworkspaceID, projectID, strings.ToLower(strings.TrimSpace(email)),\n",
              "\t\tworkspaceID, projectID, strings.ToLower(strings.TrimSpace("
              "strings.ToLower(strings.TrimSpace(email)))),\n")
# Blinds the FIXTURE, not the product: the five spellings collapse to one repeated address, so the
# test no longer depends on case or padding differing at all.
BLIND_SPELLINGS = ('\t\t"alice@acme.com", "Alice@Acme.com", "ALICE@ACME.COM",\n'
                   '\t\t" alice@acme.com", "alice@acme.com ",\n',
                   '\t\t"alice@acme.com", "alice@acme.com", "alice@acme.com",\n'
                   '\t\t"alice@acme.com", "alice@acme.com",\n')


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def refuse(msg):
    print("REFUSING: " + msg)
    sys.exit(2)


def preflight():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        refuse("TRACK_TEST_DATABASE_URL is unset; every arm would score CAUGHT for that reason.")
    out = subprocess.run(["git", "-C", REPO, "status", "--porcelain"],
                         capture_output=True, text=True).stdout.strip().splitlines()
    allowed = {"internal/member/email_identity_measured_test.go",
               "scripts/w34-member-email-identity-controls-r8x2.py"}
    dirty = [l for l in out if l[3:].strip() not in allowed]
    if dirty:
        refuse("the working tree carries changes this harness did not make:\n  " + "\n  ".join(dirty))


def apply_patch(path, old, new):
    src = open(path).read()
    n = src.count(old)
    if n != 1:
        refuse("the anchor in %s occurs %d times, not once -- the file has drifted."
               % (os.path.basename(path), n))
    patched = src.replace(old, new, 1)
    if patched == src:
        refuse("mutation changed no bytes")
    open(path, "w").write(patched)
    if os.path.getsize(path) == 0:
        refuse("mutation produced a zero-byte file")


def run(cmd):
    return subprocess.run(cmd, cwd=REPO, capture_output=True, text=True).returncode


def measure(kind):
    """CAUGHT == the population goes red."""
    if kind == "new":
        return run(["go", "test", "-timeout", "300s", "-count=1", "-run", NEWTESTS,
                    "./internal/member/"]) != 0
    stash = os.path.join(tempfile.gettempdir(), "w34-r8x2-emailtest.go")
    shutil.move(NEWTEST, stash)
    try:
        return run(["go", "test", "-timeout", "600s", "-count=1"] + PKGS) != 0
    finally:
        shutil.move(stash, NEWTEST)


ARMS = [
    ("C1", "THE FIX, port 1: AddMember canonicalises with the rule guest/store.go already uses. "
           "All three tests promise in their own failure text to notice this.",
     [(MGMT, *MGMT_IMPORT), (MGMT, *MGMT_NORMALISE)], True, False),
    ("C2", "THE FIX, port 2: the resolver keys on LOWER(email) instead of byte equality. The "
           "lockout test's PREMISE (bob cannot authenticate) stops holding, so it must red.",
     [(RESOLVER, *RESOLVER_CI)], True, False),
    ("C3", "VOID: the guest store's normalisation wrapped in itself -- a real edit that is "
           "arithmetically identity. Nothing may move, in either population.",
     [(GUEST, *GUEST_VOID)], False, False),
    ("C4", "the five spellings collapsed to one repeated address. The product is UNTOUCHED, so a "
           "red here proves the CASE AND PADDING are what the test depends on, not the count.",
     [(NEWTEST, *BLIND_SPELLINGS)], True, None),
    ("C5", "MUST STAY GREEN: no mutation at all.", [], False, False),
]


def main():
    preflight()
    files = (MGMT, RESOLVER, GUEST, NEWTEST)
    originals = {p: (sha(p), open(p).read()) for p in files}
    results = []
    try:
        for cid, desc, patches, want_new, want_old in ARMS:
            for path, old, new in patches:
                apply_patch(path, old, new)
            got_new = measure("new")
            got_old = measure("old") if want_old is not None else None
            for p in files:
                open(p, "w").write(originals[p][1])
            ok = (got_new == want_new) and (want_old is None or got_old == want_old)
            results.append((cid, ok))
            print("%s %-3s new: predicted %-7s got %-7s | old: predicted %-7s got %-7s"
                  % ("PASS" if ok else "MISPREDICTED", cid,
                     "CAUGHT" if want_new else "NOT", "CAUGHT" if got_new else "NOT",
                     "n/a" if want_old is None else ("CAUGHT" if want_old else "NOT"),
                     "n/a" if got_old is None else ("CAUGHT" if got_old else "NOT")))
            print("     " + desc)
    finally:
        for p, (digest, body) in originals.items():
            open(p, "w").write(body)
            if sha(p) != digest:
                refuse("RESTORE FAILED for %s -- sha256 does not match the pre-run one." % p)
        print("restored: all four files sha256-verified against their pre-run digests")

    good = sum(1 for _, ok in results if ok)
    print("\n%d/%d as predicted" % (good, len(results)))
    return 0 if good == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
