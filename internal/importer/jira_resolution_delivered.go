package importer

import (
	"strings"

	"github.com/talyvor/track/internal/model"
)

// jira_resolution_delivered.go — A RESOLUTION VOCABULARY IS NOT A STATUS VOCABULARY.
//
// #82 built the resolution rule on `mapJiraStatus`, deliberately and with its reason stated: reuse
// Track's own word→status table rather than invent a second one that can drift. The reuse is right
// for the words the two vocabularies share ("Done", "Closed", "Won't Fix", "Won't Do") and wrong for
// the one word that carries most of a real Jira's resolved population, because a STATUS table has no
// reason to contain a RESOLUTION-only word. `applyJiraResolution` even says so in passing — "a word
// it does not know falls to StatusBacklog … along with every recognised word that means neither" —
// and the consequence was measured, not guessed:
//
//	MEASURED 2026-08-10, REAL JIRA CLOUD (hibernate.atlassian.net, anonymous), through the endpoint
//	jiraSearchPath names. Whole population, not a sample; the vocabulary is CLOSED — the nine names
//	sum to 29,512, exactly the instance's resolved count, so nothing hides in an "other" bucket.
//
//	  resolution IS NOT EMPTY                     29,512   (= statusCategory Done, both queries agree)
//	    Fixed              19,698  66.7%   → reported "Track cannot read that word"
//	    Rejected            3,721  12.6%   → reported  (still #82's open decision)
//	    Out of Date         2,619   8.9%   → reported  (still #82's open decision)
//	    Duplicate           1,544   5.2%   → reported  (still #82's open decision)
//	    Won't Fix             951   3.2%   → acted on, done → cancelled
//	    Cannot Reproduce      518   1.8%   → reported  (still #82's open decision)
//	    Incomplete            259   0.9%   → reported  (still #82's open decision)
//	    Won't Do              149   0.5%   → acted on, done → cancelled
//	    Done                   53   0.18%  → silent
//
// So the loudest line in the import report stood for two thirds of all resolved work and told the
// operator Track could not tell whether finished work was finished. The one word the table did know
// covers 0.18% of the same population.
//
// ⚠ THIS IS NOT #82's DEFERRED DECISION AND DOES NOT TOUCH IT. That decision — whether
// "Duplicate" / "Rejected" / "Cannot Reproduce" / "Incomplete" / "Out of Date" should move a row from
// done to cancelled — changes what analytics counts as DELIVERED WORK, on 8,661 issues (29.3%) of
// the same instance. It has numbers attached and it stays exactly where #82 left it; the queue
// carries the re-measurement. "Fixed" is not in that question: it means the work WAS delivered, the
// row is already `done`, and there is nothing for it to change.
//
// ⚠ NO DATA MOVES, AND THAT IS THE PROPERTY THAT MAKES THIS A SESSION CALL RATHER THAN A DECISION.
// Both arms of applyJiraResolution's switch this word can reach return the SAME status — `case
// StatusDone` returns (status, nil), `default` returns (status, note) — and the guard above them has
// already established status == done. The only observable difference is whether a warning line
// exists. TestFixedResolutionMovesNoData pins that across every status a row can arrive with, so a
// later edit cannot quietly turn a report change into a reclassification.
//
// ⚠ THE CLASSIFICATION IS THE PROVIDER'S OWN WORDS, READ AT THE WIRE. The `resolution` object
// jiraAPIResolution already decodes carries a `description`, and on that instance it reads:
//
//	Fixed              "A fix for this issue is checked into the tree and tested."
//	Done               "Work has been completed on this issue."
//	Rejected           "The bug report describes expected behavior, or a feature will not be implemented"
//	Out of Date        "The issue is either fixed by another issue or is in some other way no longer relevant"
//	Duplicate          "The problem is a duplicate of an existing issue."
//	Cannot Reproduce   "All attempts at reproducing this issue failed …"
//	Incomplete         "The problem is not completely described."
//
// Fixed and Done are one class in the provider's own sentences and the other five are not, which is
// why exactly ONE word is added here and the rest stay reported.
//
// ⚠ THE COUNT WAS ALREADY IN THIS PACKAGE, FILED UNDER THE WRONG QUESTION. #82's pinned table has
// carried `"Fixed": {StatusDone, viaResolutionUnreadable, 13411}` — the largest row in it — under the
// heading "the decision this merge deliberately did not make". Two different questions were filed as
// one class, and the bigger of them was the one nobody was asking.

// jiraResolutionDelivered holds the resolution words that state, on their own, that the work was
// DELIVERED, and that Track's STATUS vocabulary has no reason to contain.
//
// ⚠ IT IS ONE WORD ON PURPOSE AND THE LIMIT IS STATED RATHER THAN PADDED. Every entry here has to be
// a word measured on a real instance whose own description puts it in the same class as "Done".
// "Fixed" is that on the instance measured above; "Complete"/"Completed"/"Delivered" and the rest of
// the plausible list were NOT observed there, and a word added from memory is exactly the invented
// vocabulary #82 refused. A tenant using one of those still gets a reported line, which is the
// honest outcome — it says Track did not recognise the word, and it will be true.
//
// Lower-cased at the point of comparison, like every other table in this package, so "fixed",
// "FIXED" and a padded cell all land here.
var jiraResolutionDelivered = map[string]model.IssueStatus{
	"fixed": model.StatusDone,
}

// mapJiraResolution classifies a RESOLUTION word, falling back to the shared status vocabulary.
//
// The order is load-bearing: the resolution-only table runs FIRST so it can name a word the status
// table would misfile, and the fallback runs second so every word the two vocabularies share keeps
// exactly the meaning `mapJiraStatus` has always given it — "Won't Fix" and "Won't Do" still reach
// cancellation through the one table that defines it, and no cancellation vocabulary is duplicated
// here. That is #82's anti-drift rule kept intact rather than replaced.
func mapJiraResolution(raw string) model.IssueStatus {
	if s, ok := jiraResolutionDelivered[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return s
	}
	meaning, _ := mapJiraStatus(raw)
	return meaning
}
