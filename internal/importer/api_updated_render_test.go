package importer

import (
	"strconv"
	"strings"
	"testing"
)

// api_updated_render_test.go — THE TWO API `updated` WARNINGS HAD NO SENTENCE OF THEIR OWN, AND
// RENDERED THE SAME EMPTY ONE.
//
// api_updated.go declares two vias and says, in as many words, why they are two rather than one:
// viaNoUpdatedField is "the Jira response carried no `updated` key at all", viaNullUpdatedAt is
// Linear sending null on a field it declares NON_NULL, and the file's own comment ends "A different
// sentence from Jira's absent-key case, so a different via rather than a shared one."
//
// NEITHER HAD A BRANCH IN FieldNote.render. MEASURED at 49c8a2e, both fell through to `default:`
// (`"%s — imported as %q"`) and both rendered, byte-identically:
//
//	no last-updated time value on 3 issue(s) — imported as ""
//
// Three things are wrong with that line and each is separately load-bearing:
//
//  1. THE TWO CONDITIONS ARE INDISTINGUISHABLE. The constants exist to be told apart and the
//     renderer tells the operator the same thing for both — an absent key (go and look at the
//     `fields` list this client sends) and a schema violation (go and ask Linear why a NON_NULL
//     field arrived null) send someone to two different places.
//  2. IT NAMES A VALUE THAT DOES NOT EXIST. `Mapped` is never set on these notes, so `imported as
//     ""` reports an empty string as though it were the value recorded. Nothing was imported as "".
//  3. IT STATES NO CONSEQUENCE, and the consequence is the whole reason the via exists. `updated_at`
//     is DEFAULT NOW() in Postgres, so the failure is invisible in the data: every affected issue
//     gets a plausible timestamp, sorts to the TOP of the product's main screen, and reads "updated
//     just now". viaNoUpdatedColumn — the CSV twin, three branches up — says exactly that.
//
// ⚠ WHY IT SURVIVED: this renders only on the jira_api / linear_api transports, and W3.4 records
// that no *_api import has ever been run against a real tenant. The CREATED twins
// (viaNoCreatedField, viaNullCreatedAt) both have branches; so do all three CSV Updated vias
// (viaNoUpdatedColumn, viaNoLinearUpdatedColumn, viaNoUpdatedValue). Six of the eight
// Created/Updated pairs in this switch were complete. The two that were not are the two nobody can
// reach yet.

// TestFieldNoteRender_TheAPIUpdatedWarningsAreNotTheDefaultSentence pins the three properties above.
// It asserts SHAPE rather than a byte-exact sentence so a future rewording is not a false failure —
// what must not come back is the default branch, the phantom value and the collision.
func TestFieldNoteRender_TheAPIUpdatedWarningsAreNotTheDefaultSentence(t *testing.T) {
	const count = 3
	absentKey := FieldNote{Field: fieldUpdated, Via: viaNoUpdatedField}.render(count)
	nullValue := FieldNote{Field: fieldUpdated, Via: viaNullUpdatedAt}.render(count)

	// (1) Two vias that exist to differ must differ.
	if absentKey == nullValue {
		t.Errorf("viaNoUpdatedField and viaNullUpdatedAt render the SAME sentence, so an operator "+
			"cannot tell a missing `fields` entry from a provider that violated its own non-null "+
			"schema:\n  both: %s", absentKey)
	}

	// (2) Neither may report a value it never carried. `Mapped` is unset on both, so the default
	// branch's `imported as %q` can only ever print an empty string here.
	for name, got := range map[string]string{"viaNoUpdatedField": absentKey, "viaNullUpdatedAt": nullValue} {
		if strings.Contains(got, `imported as ""`) {
			t.Errorf("%s fell through to the default branch and reports a value nothing recorded "+
				"(`imported as \"\"`):\n  %s", name, got)
		}
	}

	// (3) Each must name the consequence that makes it worth printing: updated_at is DEFAULT NOW(),
	// so the rows are not empty, they are wrong in a way that reorders the main screen.
	for name, got := range map[string]string{"viaNoUpdatedField": absentKey, "viaNullUpdatedAt": nullValue} {
		if !strings.Contains(got, "import time") {
			t.Errorf("%s does not say the issues were recorded as last updated AT IMPORT TIME, "+
				"which is the entire failure — the column is DEFAULT NOW(), so nothing looks "+
				"missing:\n  %s", name, got)
		}
	}

	// (4) Each must point at ITS OWN provider's field, the reason viaNoLinearUpdatedColumn is a
	// separate branch from viaNoUpdatedColumn: an operator sent to the wrong provider's spelling is
	// one rename from looking in the wrong place entirely.
	// ⚠ THE QUOTED FORM, NOT THE BARE NAME, AND THE FIRST DRAFT OF THIS LINE WAS WRONG. Jira's field
	// is `updated`, which is a SUBSTRING of the default sentence's own "no last-updated time value
	// on 3 issue(s)" — so `strings.Contains(absentKey, "updated")` was satisfied by the very
	// sentence this test exists to reject, and that one assertion stayed green through the red run.
	// The twin branch renders the name with %q, so requiring the quotes is both stricter and an
	// accurate description of the sentence being asked for.
	if !strings.Contains(absentKey, strconv.Quote(jiraAPIUpdatedField)) {
		t.Errorf("viaNoUpdatedField does not name Jira's %q field, so it does not say where to look:\n  %s",
			jiraAPIUpdatedField, absentKey)
	}
	if !strings.Contains(nullValue, linearAPIUpdatedField) {
		t.Errorf("viaNullUpdatedAt does not name Linear's %q field, so it does not say where to look:\n  %s",
			linearAPIUpdatedField, nullValue)
	}
}

// TestFieldNoteRender_EveryCreatedViaHasItsUpdatedTwin is the CLASS guard behind the instance above.
//
// The bug was not a wrong sentence — it was a MISSING one, and a missing branch is invisible: the
// switch still compiles, the note still renders, and the default arm answers. The only way this
// package notices the next dropped twin is to assert the pairing itself.
//
// The pairing is real and deliberate throughout this switch: viaNoCreatedColumn/viaNoUpdatedColumn,
// viaNoLinearCreatedColumn/viaNoLinearUpdatedColumn, viaNoCreatedValue/viaNoUpdatedValue,
// viaNoCreatedField/viaNoUpdatedField, viaNullCreatedAt/viaNullUpdatedAt. Each pair says the same
// thing about a different column, and each Updated sentence names the main-screen consequence its
// Created sibling does not.
func TestFieldNoteRender_EveryCreatedViaHasItsUpdatedTwin(t *testing.T) {
	pairs := []struct{ created, updated string }{
		{viaNoCreatedColumn, viaNoUpdatedColumn},
		{viaNoLinearCreatedColumn, viaNoLinearUpdatedColumn},
		{viaNoCreatedValue, viaNoUpdatedValue},
		{viaNoCreatedField, viaNoUpdatedField},
		{viaNullCreatedAt, viaNullUpdatedAt},
	}
	// The default branch is what a via with no case of its own falls into. Rendering a note with a
	// via this switch has never heard of gives us that sentence WITHOUT hard-coding its text, so
	// this guard keeps working if the default is reworded.
	defaultShape := FieldNote{Field: fieldUpdated, Via: "zz-no-such-via-5f7c"}.render(1)

	for _, p := range pairs {
		for _, via := range []string{p.created, p.updated} {
			got := FieldNote{Field: fieldUpdated, Via: via}.render(1)
			if got == defaultShape {
				t.Errorf("via %q has NO case in FieldNote.render — it falls through to the default "+
					"branch, which renders %q. Its twin %q/%q exists precisely because the two "+
					"conditions need different sentences.",
					via, defaultShape, p.created, p.updated)
			}
		}
	}
}
