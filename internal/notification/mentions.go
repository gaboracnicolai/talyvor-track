package notification

import (
	"regexp"
	"strings"
)

// mentionRe matches @handle tokens. A handle is letters/digits plus . _ - ,
// which covers email local-parts (alice.dev) and compact names (aliced).
var mentionRe = regexp.MustCompile(`@([A-Za-z0-9._-]+)`)

// parseMentions extracts the lowercased, de-duplicated set of @handles from
// free text (issue description or comment body).
//
// NOTE: Track members have no dedicated @handle field — only name and email.
// The DB-backed ResolveMentions matches each handle case-insensitively against
// the email local-part (before "@") or the name with spaces removed. This is a
// pragmatic default; if Track later adds real handles, only ResolveMentions
// needs to change.
func parseMentions(text string) []string {
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		tok := strings.ToLower(m[1])
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}
