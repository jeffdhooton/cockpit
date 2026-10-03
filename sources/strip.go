package sources

import "strings"

// StripControl removes terminal control sequences and non-printing control
// characters from captured text so a name or a log line cannot steer the
// terminal cockpit is drawn in. Tabs and newlines are kept.
func StripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == 0x1b: // ESC
			i = skipEscape(s, i)
			continue
		case c == '\n' || c == '\t':
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
			// drop
		case c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f:
			// C1 control encoded in UTF-8
			i += 2
			continue
		default:
			b.WriteByte(c)
		}
		i++
	}
	return b.String()
}

// skipEscape returns the index just past an escape sequence starting at i.
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameters, intermediates, then a final byte 0x40-0x7e
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		return i + 1
	case ']', 'P', 'X', '^', '_': // OSC / DCS / SOS / PM / APC: until BEL or ST
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}
