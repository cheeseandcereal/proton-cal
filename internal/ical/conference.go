package ical

import "regexp"

// separatorProtonEvents brackets the video-conferencing block that Proton
// embeds into an event DESCRIPTION for portability (so non-Proton clients
// still show a join link). It matches the web client's SEPARATOR_PROTON_EVENTS
// constant verbatim.
const separatorProtonEvents = "~-~-~-~-~-~-~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~-~-~-~-~-~-~"

// conferenceBlockRe matches the separator-bracketed conferencing block
// (optionally preceded by a newline), mirroring the web client's
// removeVideoConfInfoFromDescription pattern.
var conferenceBlockRe = regexp.MustCompile(
	`\n?` + regexp.QuoteMeta(separatorProtonEvents) + `[\s\S]*?` + regexp.QuoteMeta(separatorProtonEvents),
)

// StripConferenceBlock removes the Proton video-conferencing block that is
// embedded into a DESCRIPTION value, returning the user's own description
// text. It is a no-op when no such block is present. This matches how the
// Proton web/mobile clients display the description (the conference link is
// surfaced as a separate structured field instead).
func StripConferenceBlock(description string) string {
	return conferenceBlockRe.ReplaceAllString(description, "")
}
