package issue

// value_domain_doors_test.go — THE CENSUS THAT KEEPS validateValueDomain FROM BEING A ONE-DOOR RULE.
//
// value_domain_realpg_test.go proves the three doors that exist today refuse an out-of-domain
// priority and an empty title. It cannot see a FOURTH door being added tomorrow, and this
// repository's own clockguard header records five separate occasions when a rule about the issues
// table was enforced one site at a time, each fix believing it was the last.
//
// So this enumerates, from the AST, every function in the package that builds an `INSERT INTO
// issues` or `UPDATE issues` statement, and requires each to be CLASSIFIED:
//
//	gated       — it can write priority or title, and it calls validateValueDomain.
//	notAWriter  — it cannot write either column, with the reason measured from its own SET clause.
//
// A function in neither list is RED. A function in `gated` that has stopped calling the gate is
// RED. A `notAWriter` that has started naming one of the columns is RED. And a floor fails if the
// walk stops finding statements at all, because a census that finds nothing reports a clean tree.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// gated are the functions that can write priority or title. Each MUST call validateValueDomain.
var gated = map[string]string{
	"Create":             "INSERT INTO issues (… title, … priority …)",
	"UpsertByIdentifier": "INSERT … ON CONFLICT — the importer door",
	"Update":             "UPDATE issues SET %s — the dynamic SET, keys from updatableFields",
}

// notAWriter are the functions that touch the issues table and CANNOT write either column. The
// reason is measured from the statement, not assumed: each names its columns literally.
var notAWriter = map[string]string{
	"BulkUpdate":                   "SET is built from exactly status, sort_order and completed_at — a dynamic clause whose keys come from a TYPED struct, not from updatableFields",
	"Delete":                       "SET status = 'cancelled', updated_at = NOW()",
	"RecordSpendEvent":             "SET ai_cost_usd = ai_cost_usd + $4, ai_tokens = ai_tokens + $5, updated_at = NOW()",
	"RecordRequestSpendAttributed": "SET ai_cost_usd = ai_cost_usd + ins.cost_usd, …",
}

// ⚠ THE FIRST DRAFT OF THIS FILE NAMED Cancel, AddAICost AND AddAICostBulk — THREE FUNCTIONS THAT
// DO NOT EXIST — AND MISSED Delete, RecordSpendEvent AND RecordRequestSpendAttributed, WHICH DO.
// The lists above were written from memory of the file and were wrong in both directions at once;
// running the census is what said so. That is the whole argument for the census existing, made
// against its own author before it was ever merged.

const doorFloor = 6

// enclosingFuncs maps a function name to (buildsIssueWrite, callsGate, namesGatedColumn).
func TestValueDomain_EveryIssueWriteDoorIsClassified(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}

	type door struct {
		writesIssues bool
		callsGate    bool
		namesColumn  bool
	}
	doors := map[string]*door{}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		d := &door{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				lit, err := strconv.Unquote(bl.Value)
				if err != nil {
					return true
				}
				flat := strings.Join(strings.Fields(lit), " ")
				upper := strings.ToUpper(flat)
				if strings.Contains(upper, "INSERT INTO ISSUES") || strings.Contains(upper, "UPDATE ISSUES") {
					d.writesIssues = true
					if strings.Contains(flat, "priority") || strings.Contains(flat, "title") {
						d.namesColumn = true
					}
				}
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "validateValueDomain" {
					d.callsGate = true
				}
			}
			// ⚠ THE SIGNAL FOR "THIS DYNAMIC SET CAN NAME priority OR title" IS A REFERENCE TO
			// updatableFields, NOT THE SHAPE OF THE FORMAT STRING. The first draft flagged any
			// `SET %s` and therefore accused BulkUpdate, whose dynamic clause is assembled from a
			// TYPED struct (status, sort_order, completed_at) and cannot reach either column. The
			// fix that suggests itself — make BulkUpdate call the gate anyway — would have added a
			// call with nothing to check, which is a guard satisfied by doing nothing.
			if id, ok := n.(*ast.Ident); ok && id.Name == "updatableFields" {
				d.namesColumn = true
			}
			return true
		})
		if d.writesIssues {
			doors[fn.Name.Name] = d
		}
	}

	if len(doors) < doorFloor {
		t.Fatalf("the walk found only %d functions writing the issues table; the floor is %d. A "+
			"census that stops finding statements reports a clean tree rather than a broken "+
			"instrument", len(doors), doorFloor)
	}
	t.Logf("population: %d functions in store.go build an issues write", len(doors))

	for name, d := range doors {
		_, isGated := gated[name]
		_, isNot := notAWriter[name]
		switch {
		case !isGated && !isNot:
			t.Errorf("%s builds a write to the issues table and is in neither list. If it can set "+
				"priority or title it must call validateValueDomain and go in `gated`; if it "+
				"cannot, put it in `notAWriter` with the reason read off its own SET clause "+
				"(namesGatedColumn=%v)", name, d.namesColumn)
		case isGated && !d.callsGate:
			t.Errorf("%s is listed as gated and does NOT call validateValueDomain — the value "+
				"domain is unenforced on that door", name)
		case isNot && d.namesColumn:
			t.Errorf("%s is listed as not-a-writer (%q) but its statement now names priority or "+
				"title, or builds a dynamic SET. Either move it to `gated` and call the gate, or "+
				"correct the reason", name, notAWriter[name])
		}
	}

	// The other direction: a name in either list that no longer exists is a stale entry, and a
	// stale entry is how a list stops describing the tree while still looking maintained.
	for name := range gated {
		if _, ok := doors[name]; !ok {
			t.Errorf("`gated` names %s, which no longer builds an issues write. Remove it", name)
		}
	}
	for name := range notAWriter {
		if _, ok := doors[name]; !ok {
			t.Errorf("`notAWriter` names %s, which no longer builds an issues write. Remove it", name)
		}
	}
}
