package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// version_stamp_test.go — the version a running Track reports must be the one the BUILD
// stamped into it.
//
// ⚠ WHY THIS IS A BUILD TEST AND NOT AN ASSERTION ABOUT A STRING. The Dockerfile has always
// built with `-ldflags "-w -s -X main.version=${VERSION}"`, and the Go linker SILENTLY
// IGNORES -X for a symbol it cannot find — no error, no warning, the image ships. Measured
// (W3.47) before `var version` existed: that exact build linked with exit 0, the injected
// value appeared ZERO times in the binary, and the server answered
// /livez {"status":"alive","version":"0.1.0"} because both report sites carried a hardcoded
// literal. Every tag reported the same constant, and a constant looks exactly like an answer.
//
// A test that asserted the version STRING would be a third copy of a value nobody checks.
// A test that asserted "a variable named version exists" would pass on a tree where the
// linker flag no longer reaches it. So this one drives the REAL path: build through
// -ldflags with a value that cannot be a coincidence, and require the built binary to say it.

// TestVersionStamp_LdflagsReachesTheRunningBinary is the end-to-end arm.
func TestVersionStamp_LdflagsReachesTheRunningBinary(t *testing.T) {
	// ⚠ A UNIQUE PROBE AND A FRESH PATH, both deliberate: a fixed value could be satisfied by
	// a stale artefact, and a reused output path could leave the previous build in place and
	// report success for a link that never happened.
	probe := "w347-stamp-" + t.Name() + "-9f2a41"
	bin := filepath.Join(t.TempDir(), "track-probe")

	build := exec.Command("go", "build", "-ldflags", "-X main.version="+probe, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build with -ldflags failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("build reported success but produced no binary at %s: %v", bin, err)
	}

	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("`track version` failed: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != probe {
		t.Errorf("built with -X main.version=%q, binary reports %q.\n"+
			"The linker flag is not reaching the symbol the process reports — which is exactly "+
			"how this repo shipped every image announcing 0.1.0 while the Dockerfile believed it "+
			"was stamping a version.", probe, got)
	}
}

// TestVersionStamp_ReportSitesUseTheStampedSymbol is the other half: the stamp is worthless if
// the surfaces an operator actually reads hold their own literal instead. Checked structurally,
// because that is precisely how the defect looked — a working symbol next to two hardcoded
// arguments.
func TestVersionStamp_ReportSitesUseTheStampedSymbol(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// The constructors that publish a version to the outside world: /livez + /readyz bodies,
	// and the MCP serverInfo an agent reads.
	want := map[string]bool{"health.New": false, "mcp.New": false}
	ast.Inspect(f, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		name := pkg.Name + "." + sel.Sel.Name
		if _, watched := want[name]; !watched {
			return true
		}
		want[name] = true
		for _, arg := range ce.Args {
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				t.Errorf("%s at %s is passed the string literal %s — a version reported to "+
					"operators must come from the stamped `version` symbol, not a literal beside it",
					name, fset.Position(ce.Pos()), lit.Value)
			}
		}
		return true
	})

	// Non-vacuity: if a constructor were renamed or moved, this test would quietly stop
	// checking it and report success for a surface it never looked at.
	for name, seen := range want {
		if !seen {
			t.Errorf("no call to %s found in main.go — this guard is not looking at the surface "+
				"it claims to check; update it rather than leaving it vacuously green", name)
		}
	}
}
