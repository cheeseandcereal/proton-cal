package ical

import "regexp"

// separatorProtonEvents brackets the conferencing block Proton embeds into a
// DESCRIPTION; matches the web client's SEPARATOR_PROTON_EVENTS verbatim.
const separatorProtonEvents = "~-~-~-~-~-~-~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~-~-~-~-~-~-~"

// conferenceBlockRe matches the separator-bracketed conferencing block
// (optionally preceded by a newline), mirroring the web client's
// removeVideoConfInfoFromDescription pattern.
var conferenceBlockRe = regexp.MustCompile(
	`\n?` + regexp.QuoteMeta(separatorProtonEvents) + `[\s\S]*?` + regexp.QuoteMeta(separatorProtonEvents),
)

// StripConferenceBlock removes the Proton conferencing block from a
// DESCRIPTION, returning the user's own text (no-op when absent).
func StripConferenceBlock(description string) string {
	return conferenceBlockRe.ReplaceAllString(description, "")
}
