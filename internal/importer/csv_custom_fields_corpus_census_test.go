package importer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// csv_custom_fields_corpus_census_test.go — the PROVENANCE behind the numbers in
// csv_custom_fields.go, re-runnable rather than a paragraph. Same contract as the other censuses in
// this package: it drives THE SHIPPED MAPPER over the cached real corpus, it SKIPS only when the
// directory is absent, and a directory that yields no genuine export is a FAILURE.
//
// ⚠ ITS PREFIX RULE IS ITS OWN AND IS DELIBERATELY NOT jiraCustomFieldSpellings — control C5's
// finding from the unread-reference census: both sides of an equality whose subject comes from the
// thing being measured shrink together. This file re-derives "a header cell starting `Custom field
// (`" from the raw header, and excludes `Custom field (Epic Link)` by its own literal, so deleting
// the production prefix or widening the exclusion map makes the two sides DISAGREE rather than
// agree on a smaller number.
//
// ⚠⚠ AND WHICH HALF OF THIS FILE IS LOAD-BEARING IS MEASURED, NOT ASSERTED. Control D12 makes
// production silently stop reporting `Custom field (Rank)` — 12,617 rows leaving the report — and
// the PER-SPELLING EQUALITY catches it. Control D13 repeats that mutation with this file's
// exclusion DERIVED from jiraCustomFieldsReportedElsewhere, and the equality goes completely
// SILENT: every surviving spelling agrees, because both sides shrank together. What reds D13 is
// the FLOORS, and only the floors — notedRows 11,991 against ≥12,500 and exports 266 against ≥270.
// So the independent literal and the named floors catch the same defect by two different routes,
// and neither is decoration. Removing either leaves a census that can watch an entry leave.
//
// ⚠ D12 ALSO REDS TestCustomFields_TheExclusionListIsExactlyWhatAnotherEntryClaims, which needs no
// corpus at all — so an entry cannot leave this report quietly even in CI, where this file skips.
//
// ⚠ AND IT CENSUSES THE EXCLUSION, which is the half a per-column equality cannot express.
// `Custom field (Epic Link)` is populated on thousands of real rows and must produce NO
// custom-field note — it is already reported as a parent reference — while still producing its
// parent note. Both directions are asserted.

const customFieldCensusPrefix = "custom field ("

// The exclusion, by its own literal rather than by reading jiraCustomFieldsReportedElsewhere.
const customFieldCensusExcluded = "custom field (epic link)"

// Floors, not equalities: the corpus is a cache that can gain files. Every one is the measured
// figure with headroom, and each is named so a fall says WHICH instrument stopped working.
const (
	customFieldMinFiles     = 290   // 302 genuine exports at this merge
	customFieldMinRows      = 18000 // 18,807 rows
	customFieldMinNotedRows = 12500 // 13,255 rows carry ≥1 reportable custom-field value (70.5%)
	customFieldMinExports   = 270   // 282 of the 302 exports carry at least one such row
	customFieldMinSpellings = 330   // 345 distinct spellings
	customFieldMinEpicRows  = 3400  // 3,630 rows populate the excluded Epic Link column
)

func TestCustomFieldsCorpus_EveryPopulatedCellIsReportedAndTheEpicLinkIsNot(t *testing.T) {
	dir := unreadRefsJiraCorpusDir
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; csv_custom_fields_test.go "+
			"pins the same shapes as literals and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	populated, noted := map[string]int{}, map[string]int{}
	epicPopulated, epicNotedAsCustomField, epicNotedAsParent := 0, 0, 0
	notedRows, files, rows := 0, 0, 0
	exportsWithNotedRow := map[string]bool{}

	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		recs, err := readCorpusCSV(filepath.Join(dir, e.Name()))
		if err != nil || len(recs) < 2 {
			continue
		}
		have := map[string]bool{}
		for _, h := range recs[0] {
			have[strings.ToLower(strings.TrimSpace(h))] = true
		}
		if !have["summary"] || !have["status"] {
			continue
		}
		files++

		// The census's OWN prefix rule, read off the raw header rather than off columnIndex.
		censusCols := map[string]bool{}
		for _, h := range recs[0] {
			k := strings.ToLower(strings.TrimSpace(h))
			if strings.HasPrefix(k, customFieldCensusPrefix) && k != customFieldCensusExcluded {
				censusCols[k] = true
			}
		}

		ci := buildIndex(recs[0])
		for _, row := range recs[1:] {
			rows++
			m, err := jiraRowMapper(ci, row)
			if err != nil {
				continue // refused rows import nothing at all; a different class
			}
			hit := false
			for col := range censusCols {
				if len(ci.getAll(row, col)) > 0 {
					populated[col]++
					hit = true
				}
			}
			if hit {
				notedRows++
				exportsWithNotedRow[e.Name()] = true
			}
			if len(ci.getAll(row, customFieldCensusExcluded)) > 0 {
				epicPopulated++
			}
			for _, n := range m.notes {
				switch {
				case n.Field == fieldCustomFieldObj && n.Via == viaCustomFieldNotCreated:
					noted[n.Value]++
					if n.Value == customFieldCensusExcluded {
						epicNotedAsCustomField++
					}
				case n.Field == fieldParentRef && n.Value == "Custom field (Epic Link)":
					epicNotedAsParent++
				}
			}
		}
	}

	if files < customFieldMinFiles || rows < customFieldMinRows {
		t.Fatalf("corpus read %d files / %d rows, want at least %d / %d — the corpus is present but "+
			"yielded almost nothing, which is an instrument failure, not a clean answer",
			files, rows, customFieldMinFiles, customFieldMinRows)
	}
	if notedRows < customFieldMinNotedRows {
		t.Errorf("rows carrying a reportable custom field = %d, want ≥%d — either the corpus "+
			"changed or the census stopped finding the columns", notedRows, customFieldMinNotedRows)
	}
	if len(exportsWithNotedRow) < customFieldMinExports {
		t.Errorf("exports carrying such a row = %d, want ≥%d", len(exportsWithNotedRow), customFieldMinExports)
	}
	if len(populated) < customFieldMinSpellings {
		t.Errorf("distinct populated spellings = %d, want ≥%d", len(populated), customFieldMinSpellings)
	}

	// THE EQUALITY, PER SPELLING. The report and the loss must be the same set of rows — for all
	// 345 of them, not for a sampled few.
	mismatched := []string{}
	for col, n := range populated {
		if noted[col] != n {
			mismatched = append(mismatched, col)
		}
	}
	for col, n := range noted {
		if populated[col] != n {
			mismatched = append(mismatched, col)
		}
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		show := mismatched
		if len(show) > 8 {
			show = show[:8]
		}
		for _, col := range show {
			t.Errorf("%q populated on %d rows but reported on %d — the report and the loss are not "+
				"the same set of rows", col, populated[col], noted[col])
		}
		t.Errorf("%d spelling(s) disagree in total", len(mismatched))
	}

	// THE EXCLUSION, ASSERTED IN BOTH DIRECTIONS. It is populated on real rows; it must produce no
	// custom-field note AND must still produce its parent note, or the exclusion silenced a report
	// that already existed instead of preventing a second one.
	if epicPopulated < customFieldMinEpicRows {
		t.Errorf("only %d row(s) populate %q, so its exclusion is barely tested — want ≥%d",
			epicPopulated, customFieldCensusExcluded, customFieldMinEpicRows)
	}
	if epicNotedAsCustomField != 0 {
		t.Errorf("%q produced %d custom-field note(s) on top of its parent note — one dropped link, "+
			"two sentences", customFieldCensusExcluded, epicNotedAsCustomField)
	}
	if epicNotedAsParent == 0 {
		t.Errorf("%q is populated on %d rows and produced NO parent note — the exclusion silenced "+
			"the report that already existed", customFieldCensusExcluded, epicPopulated)
	}

	top := make([]string, 0, len(populated))
	for col := range populated {
		top = append(top, col)
	}
	sort.Slice(top, func(i, j int) bool { return populated[top[i]] > populated[top[j]] })
	if len(top) > 6 {
		top = top[:6]
	}
	t.Logf("files=%d rows=%d | rows with a reportable custom field=%d (%.1f%%) in %d exports over %d spellings",
		files, rows, notedRows, 100*float64(notedRows)/float64(rows), len(exportsWithNotedRow), len(populated))
	for _, col := range top {
		t.Logf("   %-44s populated=%6d reported=%6d", col, populated[col], noted[col])
	}
	t.Logf("   excluded %-35s populated=%6d as-custom-field=%d as-parent=%d",
		customFieldCensusExcluded, epicPopulated, epicNotedAsCustomField, epicNotedAsParent)
}
