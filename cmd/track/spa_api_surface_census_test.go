package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// spa_api_surface_census_test.go — every HTTP request the shipped SPA issues, against every
// route the router registers, MATCHED ON METHOD AND PATH, in both directions.
//
// THE TWO RESULTS, MEASURED AT 2690f41 (136 registered routes, 66 distinct SPA verb+path pairs):
//
//	(a) SPA → server:  ZERO. Every request the SPA makes reaches a registered route with a
//	    matching method. Nothing 404s, nothing 405s. TestEverySPARequestReachesARegisteredRoute
//	    is what keeps that true — a mistyped path in the SPA typechecks perfectly and fails only
//	    at runtime, which is why nothing else in this repository could catch it.
//
//	(b) server → SPA:  70 of 136 registered routes have NO SPA caller, and they are pinned in
//	    routesWithNoSPACaller below WITH WHAT DOES REACH THEM. Most are legitimate: probes,
//	    webhooks an external service POSTs, the MCP transport, and the /v1/service/* pair that
//	    talyvor-docs and talyvor-suite call. **35 of the 70 are reached by nothing, anywhere.**
//
// ⚠ HOW (b) WAS MEASURED AND WHERE ITS EVIDENCE STOPS, because half of it CI cannot see. The
// SPA half is measured here, in CI, on every run. The "anywhere in the estate" half was measured
// once, out of CI, by regex-matching each path against talyvor-lens, talyvor-docs, talyvor-code,
// talyvor-suite, talyvor-research and this repository's own non-Go assets — CI checks out only
// talyvor-track, so it CANNOT re-verify that half and this file does not pretend it does. The
// estate search is also PATH-level, not verb-level: a tag saying a path appears in talyvor-suite
// means the path was found there, NOT that the verb was matched. Both limits are written into
// the tags themselves rather than left for a reader to infer.
//
// ⚠ WHAT (b) IS AND IS NOT. "No SPA caller" is not "dead" — this SPA is admittedly partial
// (api/client.ts: "Phase 8 doesn't ship a login flow — Phase 9 will"). The pin exists so that
// BOTH directions of change announce themselves: wiring a UI to one of these shrinks the set and
// reds, and adding another route nothing calls grows it and reds. W3.14 found ONE such subtree by
// hand (`grep -rn "workflow" frontend/src` = 0 hits); this is the whole surface, and the four
// /statuses routes below are that same finding arriving from the other direction.
//
// ⚠ TWO FINDINGS IN THE 35 THAT ARE WORTH READING BEFORE THE TABLE:
//
//	1. THE GUEST `commenter` ROLE HAS NO WAY TO COMMENT. Eight guest/invite routes ship; the SPA
//	   calls SEVEN. The one it does not is `POST /v1/guest/workspaces/{}/issues/{}/comments` —
//	   the single route that distinguishes `commenter` and `editor` from `viewer`. The product
//	   mints access tokens carrying those roles (internal/guest signs Role into the HMAC claims)
//	   and no client in the estate can exercise either of them.
//
//	2. ALL FOUR MILESTONE ROUTES ARE UNREACHED, AND THAT SETTLES A QUESTION W3.25 LEFT OPEN.
//	   The SPA HAS milestone UI (components/roadmap/MilestoneMarker.tsx) and gets its data from
//	   `/v1/workspaces/{}/roadmap`, whose RoadmapMilestone EMBEDS milestone.Milestone. So it
//	   READS milestones and cannot WRITE them. W3.25 measured `milestones.completed_at` as
//	   "not a finding" because internal/milestone's `updatable` allow-list contains it — true of
//	   the code, and the reachability census says the only route that reaches that allow-list,
//	   `PATCH .../milestones/{id}`, is called by nothing. The column is writable in principle and
//	   unwritten in practice, which is what `guests.last_seen_at` was found to be structurally.
//
// ⚠ MEASURED, NOT ASSUMED, BECAUSE IT WOULD HAVE BEEN A FALSE FINDING: 29 routes are registered
// with a TRAILING SLASH (`r.Route("/issues", …); r.Get("/", …)` yields `/v1/…/issues/`) and the
// SPA spells them without one. chi serves BOTH — driven through a real chi.Mux, `/issues` and
// `/issues/` each returned 200 from the same handler — so the difference is notation and the
// normaliser below folds it deliberately, not for convenience.
//
// Controls: ~/talyvor-queue/w326-surface-controls-x7p2.py — 8/8 armed, every mutation
// compile-checked before it runs. P1 a call to a path no route serves → red (404 class) · P2 the
// right path with a method the router does not serve → red, reported DISTINCTLY (405 class) ·
// P3 frontend/src/api removed → the scanner floor reds · P4 a UI wired to a table entry → red ·
// P5 a new route nothing calls → red · P6 a bogus /v1 path in a frontend COMMENT → GREEN ·
// P7 the table emptied → red · P8 unmutated → GREEN.
//
// ⚠ P3 WAS NOT ARMED ON ITS FIRST PASS AND IT IS THE SAME SHAPE THIS QUEUE KEEPS CATCHING. It
// moved frontend/src/api to frontend/src/api.w326bak — still inside the directory the scanner
// walks — so every file was still found, the census was unchanged, and the control reported a
// clean run while measuring nothing. A control has to remove the thing from the SCAN, not from
// its name.

// ── the SPA-side scanner ────────────────────────────────────────────────────────────────────────
//
// ⚠ IT IS NOT A `grep` FOR apiRequest, AND THE FIRST VERSION THAT WAS GOT THIS WRONG TWICE. Anchoring
// on `apiRequest<T>(` missed every call whose type argument itself contains angle brackets
// (`apiRequest<Array<{…}>>`), silently dropping three paths; and attributing the method from a
// fixed-size window after the literal read the NEXT function's `method:` and manufactured nine
// method mismatches that do not exist. What follows scans LITERALS, not call shapes, and bounds
// each method window at the next /v1 literal.

type spaReq struct {
	Verb string
	Path string
	File string
}

var (
	reTmplLit  = regexp.MustCompile("`[^`]*`|\"/v1[^\"]*\"|'/v1[^']*'")
	reMethodKV = regexp.MustCompile(`method\s*:\s*["']([A-Za-z]+)["']`)
	reInterpQS = regexp.MustCompile(`\$\{qs\([\s\S]*?\)\}`)
	reInterp   = regexp.MustCompile(`\$\{[^}]*\}`)
	reParam    = regexp.MustCompile(`\{[^}]*\}`)
)

// stripTSComments removes // and /* */ comments without touching string or template literals, so a
// `/v1/…` path mentioned in a comment is not counted as a request. Control P6 covers this.
func stripTSComments(src string) string {
	var b strings.Builder
	mode := byte(0)
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch mode {
		case 0:
			if c == '/' && i+1 < len(src) && src[i+1] == '/' {
				mode = 'L'
				i++
				continue
			}
			if c == '/' && i+1 < len(src) && src[i+1] == '*' {
				mode = 'B'
				i++
				continue
			}
			if c == '`' || c == '"' || c == '\'' {
				mode = c
			}
			b.WriteByte(c)
		case 'L':
			if c == '\n' {
				mode = 0
				b.WriteByte(c)
			}
		case 'B':
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				mode = 0
				i++
				b.WriteByte(' ')
				continue
			}
			if c == '\n' {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				b.WriteByte(src[i+1])
				i++
				continue
			}
			if c == mode {
				mode = 0
			}
		}
	}
	return b.String()
}

// normPath folds an SPA template literal and a chi pattern onto the same shape: every parameter
// becomes {}, the query string goes, and a trailing slash is dropped (chi serves both — measured).
func normPath(s string) string {
	s = reInterpQS.ReplaceAllString(s, "")
	s = reInterp.ReplaceAllString(s, "{}")
	s = reParam.ReplaceAllString(s, "{}")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 1 && strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}

func spaRequests(t *testing.T) []spaReq {
	t.Helper()
	root := filepath.Join(repoRoot(t), "frontend", "src")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("frontend/src not found at %s: %v", root, err)
	}
	var out []spaReq
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx")) {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		src := stripTSComments(string(raw))
		rel, _ := filepath.Rel(root, p)

		var hits [][]int
		for _, m := range reTmplLit.FindAllStringSubmatchIndex(src, -1) {
			if strings.Contains(src[m[0]:m[1]], "/v1") {
				hits = append(hits, m)
			}
		}
		for k, m := range hits {
			lit := strings.Trim(src[m[0]:m[1]], "`\"'")
			idx := strings.Index(lit, "/v1")
			path := normPath(lit[idx:])
			// ⚠ THE WINDOW IS BOUNDED BY THE NEXT /v1 LITERAL, NOT A CHARACTER COUNT — see the
			// note above; a fixed window reads the next function's method and invents mismatches.
			stop := len(src)
			if k+1 < len(hits) {
				stop = hits[k+1][0]
			}
			if m[1]+400 < stop {
				stop = m[1] + 400
			}
			verb := "GET"
			if mm := reMethodKV.FindStringSubmatch(src[m[1]:stop]); mm != nil {
				verb = strings.ToUpper(mm[1])
			}
			// A WebSocket upgrade is served by a chi Get; the transport differs, the route does not.
			if strings.Contains(src[m[1]:min(m[1]+120, len(src))], "WebSocket") || strings.Contains(lit, "BASE_WS") {
				verb = "GET"
			}
			out = append(out, spaReq{Verb: verb, Path: path, File: rel})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk frontend/src: %v", err)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// serverSurface maps "VERB /normalised/path" -> the route as registered, and path -> verbs served.
func serverSurface(t *testing.T) (map[string]string, map[string][]string) {
	t.Helper()
	byPair := map[string]string{}
	byPath := map[string][]string{}
	for _, r := range allRoutes(t) {
		parts := strings.SplitN(r, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("unparsable route %q from allRoutes", r)
		}
		verb, path := strings.ToUpper(parts[0]), normPath(parts[1])
		byPair[verb+" "+path] = r
		byPath[path] = append(byPath[path], verb)
	}
	return byPair, byPath
}

// ⚠ FLOORS. Both censuses report an ABSENCE, and an absence is what a broken scanner reports too.
// These are the counts measured at 2690f41; if either scanner stops seeing what it saw then, it has
// gone blind and must fail rather than quietly agree with itself.
const (
	minSPARequestSites = 60 // 66 distinct verb+path pairs measured; floored below that, not at it
	minSPAFilesWithV1  = 10 // 11 files under frontend/src name a /v1 path
)

// ── (a) every SPA request reaches a registered route ────────────────────────────────────────────

func TestEverySPARequestReachesARegisteredRoute(t *testing.T) {
	reqs := spaRequests(t)
	byPair, byPath := serverSurface(t)

	pairs := map[string]bool{}
	files := map[string]bool{}
	for _, r := range reqs {
		pairs[r.Verb+" "+r.Path] = true
		files[r.File] = true
	}
	if len(pairs) < minSPARequestSites || len(files) < minSPAFilesWithV1 {
		t.Fatalf("the SPA scanner went blind: %d distinct verb+path pairs across %d files, "+
			"want >=%d across >=%d. At 2690f41 it found 66 across 11. Do not lower these to "+
			"make a red go green — find out why the scan stopped seeing the SPA.",
			len(pairs), len(files), minSPARequestSites, minSPAFilesWithV1)
	}

	var missing, mismatched []string
	seen := map[string]bool{}
	for _, r := range reqs {
		key := r.Verb + " " + r.Path
		if byPair[key] != "" || seen[key] {
			seen[key] = true
			continue
		}
		seen[key] = true
		if verbs, ok := byPath[r.Path]; ok {
			sort.Strings(verbs)
			mismatched = append(mismatched, fmt.Sprintf("%s (%s) — server serves %s on that path",
				key, r.File, strings.Join(verbs, ",")))
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (%s) — no route with that path at all", key, r.File))
	}
	sort.Strings(missing)
	sort.Strings(mismatched)
	if len(missing) > 0 {
		t.Errorf("%d SPA request(s) hit a path the router does not serve — a runtime 404 that "+
			"typechecks cleanly:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
	if len(mismatched) > 0 {
		t.Errorf("%d SPA request(s) use a method the router does not serve on that path — a "+
			"runtime 405:\n  %s", len(mismatched), strings.Join(mismatched, "\n  "))
	}
}

// ── (b) the routes no SPA call reaches, and what does ───────────────────────────────────────────
//
// ⚠ THE VALUE OF THIS TABLE IS THE RIGHT-HAND COLUMN, NOT THE LEFT. "70 routes have no SPA caller"
// is a number; "35 of them are reached by nothing, anywhere in the estate" is a finding, and the
// only way to tell those apart is to write down what reaches each one. A bare count would have
// hidden the guest-commenter and milestone findings in the header completely.
//
// ⚠ THE ESTATE TAGS ARE A RECORDED MEASUREMENT, NOT A CI-VERIFIED CLAIM — CI checks out only this
// repository. They were taken at 2690f41 against the five sibling repos and this repo's non-Go
// assets, PATH-level (the verb was not matched). The test below verifies ONLY the left-hand set.

var routesWithNoSPACaller = map[string]string{
	"DELETE /v1/workspaces/{}":                       "path appears in talyvor-code,talyvor-docs,talyvor-lens,talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"DELETE /v1/workspaces/{}/automation/rules/{}":   "NO CALLER ANYWHERE IN THE ESTATE",
	"DELETE /v1/workspaces/{}/issues/{}/comments/{}": "NO CALLER ANYWHERE IN THE ESTATE",
	"DELETE /v1/workspaces/{}/labels/{}":             "NO CALLER ANYWHERE IN THE ESTATE",
	"DELETE /v1/workspaces/{}/members/{}":            "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"DELETE /v1/workspaces/{}/projects/{}":           "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"DELETE /v1/workspaces/{}/teams/{}":              "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"DELETE /v1/workspaces/{}/teams/{}/statuses/{}":  "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /healthz":                                             "probe — deploy/k8s only",
	"GET /livez":                                               "probe — deploy/k8s only",
	"GET /mcp/sse":                                             "MCP transport — agents, not the SPA",
	"GET /readyz":                                              "probe — deploy/k8s only",
	"GET /v1/import/jobs/{}":                                   "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"GET /v1/integrations/{}":                                  "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/service/members":                                  "service-to-service — talyvor-docs/suite call it",
	"GET /v1/service/workspaces":                               "service-to-service — talyvor-docs/suite call it",
	"GET /v1/workspaces":                                       "path appears in talyvor-code,talyvor-docs,talyvor-lens,talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"GET /v1/workspaces/{}":                                    "path appears in talyvor-code,talyvor-docs,talyvor-lens,talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"GET /v1/workspaces/{}/ai-costs":                           "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/analytics/export":                   "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/analytics/resolution":               "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/automation/logs":                    "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/automation/rules":                   "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/issues/by-identifier/{}":            "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/issues/{}/ai-costs":                 "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/issues/{}/comments":                 "path appears in talyvor-code,talyvor-suite (PATH-level grep; the verb was NOT matched)",
	"GET /v1/workspaces/{}/issues/{}/summary":                  "path appears in talyvor-suite (PATH-level grep; the verb was NOT matched)",
	"GET /v1/workspaces/{}/labels":                             "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/members":                            "path appears in talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"GET /v1/workspaces/{}/notifications":                      "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/projects/{}/milestones":             "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/projects/{}/milestones/{}/progress": "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/teams/{}/cycles":                    "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/teams/{}/cycles/active":             "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/teams/{}/cycles/{}/burndown":        "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/teams/{}/cycles/{}/progress":        "NO CALLER ANYWHERE IN THE ESTATE",
	"GET /v1/workspaces/{}/teams/{}/statuses":                  "NO CALLER ANYWHERE IN THE ESTATE",
	"PATCH /v1/workspaces/{}":                                  "path appears in talyvor-code,talyvor-docs,talyvor-lens,talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"PATCH /v1/workspaces/{}/issues/{}/comments/{}":            "NO CALLER ANYWHERE IN THE ESTATE",
	"PATCH /v1/workspaces/{}/members/{}":                       "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"PATCH /v1/workspaces/{}/projects/{}":                      "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"PATCH /v1/workspaces/{}/projects/{}/milestones/{}":        "NO CALLER ANYWHERE IN THE ESTATE",
	"PATCH /v1/workspaces/{}/teams/{}":                         "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"PATCH /v1/workspaces/{}/teams/{}/cycles/{}":               "NO CALLER ANYWHERE IN THE ESTATE",
	"PATCH /v1/workspaces/{}/teams/{}/statuses/{}":             "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /mcp":          "MCP transport — agents, not the SPA",
	"POST /v1/bootstrap": "path appears in talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/guest/workspaces/{}/issues/{}/comments":          "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/import/jira":                                     "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/import/jobs":                                     "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/import/linear":                                   "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/integrations":                                    "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/lens/webhook":                                    "webhook — an external service POSTs it",
	"POST /v1/webhooks/github":                                 "webhook — an external service POSTs it",
	"POST /v1/workspaces":                                      "path appears in talyvor-code,talyvor-docs,talyvor-lens,talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/automation/rules":                  "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/issues/{}/comments":                "path appears in talyvor-code,talyvor-suite (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/issues/{}/find-duplicates":         "path appears in talyvor-suite (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/issues/{}/triage":                  "path appears in talyvor-suite (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/labels":                            "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/members":                           "path appears in talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/notifications/read-all":            "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/notifications/{}/read":             "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/projects":                          "path appears in track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/projects/{}/milestones":            "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/teams":                             "path appears in talyvor-suite,track-nonsrc (PATH-level grep; the verb was NOT matched)",
	"POST /v1/workspaces/{}/teams/{}/cycles":                   "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/teams/{}/cycles/{}/complete":       "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/teams/{}/cycles/{}/suggest-issues": "NO CALLER ANYWHERE IN THE ESTATE",
	"POST /v1/workspaces/{}/teams/{}/statuses":                 "NO CALLER ANYWHERE IN THE ESTATE",
}

func TestRoutesWithNoSPACallerAreTheKnownSet(t *testing.T) {
	reqs := spaRequests(t)
	byPair, _ := serverSurface(t)

	called := map[string]bool{}
	for _, r := range reqs {
		called[r.Verb+" "+r.Path] = true
	}
	got := map[string]bool{}
	for pair := range byPair {
		if !called[pair] {
			got[pair] = true
		}
	}

	var appeared, disappeared []string
	for pair := range got {
		if _, ok := routesWithNoSPACaller[pair]; !ok {
			appeared = append(appeared, pair)
		}
	}
	for pair := range routesWithNoSPACaller {
		if !got[pair] {
			disappeared = append(disappeared, pair)
		}
	}
	sort.Strings(appeared)
	sort.Strings(disappeared)

	if len(appeared) > 0 {
		t.Errorf("%d route(s) are now registered with NO SPA caller and are not in the table:\n  %s\n\n"+
			"Add each one with what DOES reach it — a probe, a webhook, a sibling repo, or "+
			"\"NO CALLER ANYWHERE IN THE ESTATE\" if you checked and found none. The tag is the "+
			"point of the table; an entry with no reason is worse than no entry.",
			len(appeared), strings.Join(appeared, "\n  "))
	}
	if len(disappeared) > 0 {
		t.Errorf("%d route(s) in the table NOW HAVE an SPA caller:\n  %s\n\n"+
			"That is almost certainly good news — a UI was wired to something that had none. "+
			"Delete those rows. This is the direction the table exists to notice.",
			len(disappeared), strings.Join(disappeared, "\n  "))
	}

	// ⚠ A COUNT FLOOR ON THE TABLE ITSELF. Without it, deleting the whole table makes both loops
	// above vacuous and the test passes green on an empty census.
	if len(routesWithNoSPACaller) < 60 {
		t.Fatalf("the table has %d entries; 70 were measured at 2690f41. Shrinking it below 60 "+
			"means routes were wired up en masse or the table was gutted — say which.",
			len(routesWithNoSPACaller))
	}
	nowhere := 0
	for _, tag := range routesWithNoSPACaller {
		if strings.HasPrefix(tag, "NO CALLER ANYWHERE") {
			nowhere++
		}
	}
	if nowhere == 0 {
		t.Error("no row is tagged NO CALLER ANYWHERE IN THE ESTATE. 35 were at 2690f41; if every " +
			"one has since found a caller that is a real and welcome change, but it should be " +
			"stated in the header rather than left as an empty category.")
	}
}
