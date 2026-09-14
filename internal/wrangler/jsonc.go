package wrangler

import "strings"

// stripComments removes JSONC comments, leaving valid JSON.
//
// # Why this is a real scanner
//
// The JavaScript this replaces dropped any line whose first non-space
// characters were "//", and documented the consequence: a comment appended
// after a value on the same line survived, and a "://" inside a URL string on
// its own line would be eaten. It also could not handle block comments at all.
//
// That last part is not hypothetical. `wrangler init` generates a
// wrangler.jsonc containing /** ... */ blocks, so the common case — a config
// Cloudflare's own tooling produced — failed to parse.
//
// Scanning properly costs about thirty lines and removes the whole class:
// comments are recognised only outside string literals, escapes are honoured
// so a \" does not appear to end a string, and both comment forms are
// handled. Content is replaced rather than deleted where it keeps byte
// offsets stable for JSON's own error messages: a block comment becomes
// spaces, so a syntax error still points at the right column.
func stripComments(text string) string {
	var out strings.Builder
	out.Grow(len(text))

	const (
		code = iota
		inString
		inLineComment
		inBlockComment
	)
	state := code
	escaped := false

	for i := 0; i < len(text); i++ {
		c := text[i]
		switch state {
		case code:
			switch {
			case c == '"':
				state = inString
				out.WriteByte(c)
			case c == '/' && i+1 < len(text) && text[i+1] == '/':
				state = inLineComment
				i++
			case c == '/' && i+1 < len(text) && text[i+1] == '*':
				state = inBlockComment
				i++
			default:
				out.WriteByte(c)
			}

		case inString:
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				state = code
			}

		case inLineComment:
			// The newline itself is kept, so line numbers in a JSON parse
			// error still line up with the file on disk.
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}

		case inBlockComment:
			if c == '*' && i+1 < len(text) && text[i+1] == '/' {
				state = code
				i++
				continue
			}
			// Newlines inside a block comment are preserved for the same
			// reason.
			if c == '\n' {
				out.WriteByte(c)
			}
		}
	}
	return out.String()
}
