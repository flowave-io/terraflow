package cli

import (
	"strings"
	"unicode"
)

func NormalizeCommasInMultiline(input string) string {
	if !strings.Contains(input, "\n") {
		return input
	}

	type frame struct {
		opener               rune // '[' or '{'
		indexInStack         int  // position in stack when pushed
		lastSigAtTopLevel    rune // last non-space/comment/significant rune seen at base depth of this frame
		lineHasTopLevelToken bool // whether current line has any token at base depth
		insertedAnyComma     bool // whether we inserted any comma inside this frame due to multiline handling
	}

	var out []rune
	var stack []rune // track all openers: '(', '[', '{'
	var frames []frame
	runes := []rune(input)

	inString := false
	stringEsc := false
	inLineComment := false
	inBlockComment := false

	// helper to know if we are at base depth for the current comma-managed frame
	atBaseDepth := func() bool {
		if len(frames) == 0 {
			return false
		}
		top := frames[len(frames)-1]
		return len(stack) == top.indexInStack+1 && stack[len(stack)-1] == top.opener
	}

	// insert a comma before any trailing spaces/tabs at the end of current out buffer
	insertCommaBeforeTrailingSpace := func() {
		j := len(out) - 1
		for j >= 0 && (out[j] == ' ' || out[j] == '\t') {
			j--
		}
		// If the last non-space is a newline, insert before that newline
		if j >= 0 && out[j] == '\n' {
			pos := j
			out = append(out, 0)
			copy(out[pos+1:], out[pos:])
			out[pos] = ','
			return
		}
		// otherwise insert at position j+1
		if j+1 >= len(out) {
			out = append(out, ',')
			return
		}
		out = append(out, 0)
		copy(out[j+2:], out[j+1:])
		out[j+1] = ','
	}

	// insert a comma at the end of the previous line if the buffer currently ends at a newline,
	// otherwise behave like insertCommaBeforeTrailingSpace.
	insertCommaAtLineEnd := func() {
		if len(out) == 0 {
			out = append(out, ',')
			return
		}
		if out[len(out)-1] == '\n' {
			pos := len(out) - 1 // insert before this newline
			out = append(out, 0)
			copy(out[pos+1:], out[pos:])
			out[pos] = ','
			return
		}
		insertCommaBeforeTrailingSpace()
	}

	// helper to update last significant rune at base depth
	setLastSig := func(r rune) {
		if len(frames) == 0 {
			return
		}
		// only update when at base depth
		if atBaseDepth() {
			f := frames[len(frames)-1]
			f.lastSigAtTopLevel = r
			frames[len(frames)-1] = f
		}
	}

	// mark that we saw some content on this line at base depth
	markTopLevelToken := func() {
		if len(frames) == 0 {
			return
		}
		if atBaseDepth() {
			f := frames[len(frames)-1]
			f.lineHasTopLevelToken = true
			frames[len(frames)-1] = f
		}
	}

	// When encountering a newline at base depth inside {} or [], add comma if:
	// - line had a token at base depth
	// - last significant isn't ',' or the opener
	// - last significant isn't '=' (avoid breaking multi-line assignments)
	// Find the next significant rune after position i, skipping whitespace and comments.
	// Returns the rune and also the next rune (for two-char operators like '=>').
	nextSignificantAfter := func(i int) (rune, rune) {
		j := i + 1
		for j < len(runes) {
			r := runes[j]
			// skip whitespace except '\n' which is a hard break we just saw
			if r == ' ' || r == '\t' || r == '\r' {
				j++
				continue
			}
			// line comment starting at this position
			if r == '#' {
				// skip to end of line
				for j < len(runes) && runes[j] != '\n' {
					j++
				}
				continue
			}
			if r == '/' && j+1 < len(runes) {
				if runes[j+1] == '/' {
					for j < len(runes) && runes[j] != '\n' {
						j++
					}
					continue
				}
				if runes[j+1] == '*' {
					// skip block comment
					j += 2
					for j < len(runes) {
						if runes[j] == '/' && j-1 >= 0 && runes[j-1] == '*' {
							j++
							break
						}
						j++
					}
					continue
				}
			}
			// found next sig
			var next rune
			if j+1 < len(runes) {
				next = runes[j+1]
			}
			return r, next
		}
		return 0, 0
	}

	handleNewline := func(i int) {
		if len(frames) == 0 {
			return
		}
		if !atBaseDepth() {
			return
		}
		f := frames[len(frames)-1]
		if !f.lineHasTopLevelToken {
			return
		}
		if f.lastSigAtTopLevel == ',' || f.lastSigAtTopLevel == f.opener || f.lastSigAtTopLevel == '=' || f.lastSigAtTopLevel == ':' || f.lastSigAtTopLevel == '>' {
			// do not insert
			f.lineHasTopLevelToken = false
			frames[len(frames)-1] = f
			return
		}
		// Do not insert if the next significant token after this newline is ':' or '=>'
		if nx, nx2 := nextSignificantAfter(i); nx == ':' || (nx == '=' && nx2 == '>') {
			f.lineHasTopLevelToken = false
			frames[len(frames)-1] = f
			return
		}
		// If the next token closes the current frame, do not insert (handles both '}' and ']')
		if nx, _ := nextSignificantAfter(i); (frames[len(frames)-1].opener == '{' && nx == '}') || (frames[len(frames)-1].opener == '[' && nx == ']') {
			f.lineHasTopLevelToken = false
			frames[len(frames)-1] = f
			return
		}
		// insert comma just before newline (spaces already emitted stay before comma)
		out = append(out, ',')
		f.lastSigAtTopLevel = ','
		f.lineHasTopLevelToken = false
		f.insertedAnyComma = true
		frames[len(frames)-1] = f
	}

	// Before closing a frame with ] or }, insert trailing comma if needed.
	handleBeforeClose := func() {
		if len(frames) == 0 {
			return
		}
		if !atBaseDepth() {
			return
		}
		f := frames[len(frames)-1]
		// Only add trailing comma for [] (lists/tuples) and only if we already inserted
		// inter-item commas in that list. Never add trailing commas for {} (objects/maps).
		if f.opener == '[' &&
			f.lastSigAtTopLevel != ',' &&
			f.lastSigAtTopLevel != f.opener &&
			f.lastSigAtTopLevel != 0 &&
			f.insertedAnyComma {
			insertCommaAtLineEnd()
			f.lastSigAtTopLevel = ','
			frames[len(frames)-1] = f
		}
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// Handle end-of-line comment mode
		if inLineComment {
			// Emit comment runes; on newline, decide comma insertion before writing the newline.
			if r == '\n' {
				// At end of a commented line, apply newline handling (based on tokens before the comment)
				handleNewline(i)
				out = append(out, r)
				inLineComment = false
				// Reset line token state for the next line at this depth
				if len(frames) > 0 && atBaseDepth() {
					f := frames[len(frames)-1]
					f.lineHasTopLevelToken = false
					frames[len(frames)-1] = f
				}
			} else {
				out = append(out, r)
			}
			continue
		}
		// Handle block comment mode
		if inBlockComment {
			out = append(out, r)
			// detect end of block comment */
			if r == '/' && len(out) >= 2 && out[len(out)-2] == '*' {
				inBlockComment = false
			}
			continue
		}

		// Handle string mode (only double-quoted HCL strings)
		if inString {
			out = append(out, r)
			if stringEsc {
				stringEsc = false
				continue
			}
			if r == '\\' {
				stringEsc = true
				continue
			}
			if r == '"' {
				inString = false
				// a string end is a significant token at base depth
				setLastSig('"')
				markTopLevelToken()
			}
			continue
		}

		// Detect start of comments
		if r == '#' {
			// Before starting a comment at base depth, ensure prior item is comma-terminated
			if len(frames) > 0 && atBaseDepth() {
				f := frames[len(frames)-1]
				if f.lineHasTopLevelToken && f.lastSigAtTopLevel != ',' && f.lastSigAtTopLevel != f.opener && f.lastSigAtTopLevel != '=' {
					insertCommaBeforeTrailingSpace()
					f.lastSigAtTopLevel = ','
					frames[len(frames)-1] = f
				}
			}
			inLineComment = true
			out = append(out, r)
			continue
		}
		if r == '/' && i+1 < len(runes) {
			if runes[i+1] == '/' {
				// Before starting a comment at base depth, ensure prior item is comma-terminated
				if len(frames) > 0 && atBaseDepth() {
					f := frames[len(frames)-1]
					if f.lineHasTopLevelToken && f.lastSigAtTopLevel != ',' && f.lastSigAtTopLevel != f.opener && f.lastSigAtTopLevel != '=' {
						insertCommaBeforeTrailingSpace()
						f.lastSigAtTopLevel = ','
						frames[len(frames)-1] = f
					}
				}
				inLineComment = true
				out = append(out, r)
				continue
			}
			if runes[i+1] == '*' {
				inBlockComment = true
				out = append(out, r)
				continue
			}
		}

		switch r {
		case '"':
			inString = true
			out = append(out, r)
			continue
		case '\n':
			// Decide whether to add a comma before newline
			handleNewline(i)
			out = append(out, r)
			continue
		case '(':
			// track parentheses to avoid treating content as top-level
			stack = append(stack, '(')
			out = append(out, r)
			continue
		case ')':
			// pop last '(' if present
			if len(stack) > 0 && stack[len(stack)-1] == '(' {
				stack = stack[:len(stack)-1]
			}
			out = append(out, r)
			continue
		case '[', '{':
			// push to general stack
			stack = append(stack, r)
			// Only manage comma insertion for [] and {}
			frames = append(frames, frame{
				opener:               r,
				indexInStack:         len(stack) - 1,
				lastSigAtTopLevel:    r, // opener acts as last sig initially
				lineHasTopLevelToken: false,
			})
			out = append(out, r)
			continue
		case ']', '}':
			// Before closing, add trailing comma if needed at base depth
			handleBeforeClose()
			// pop matching from general stack
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			// pop frame if matching type
			if len(frames) > 0 && ((r == ']' && frames[len(frames)-1].opener == '[') || (r == '}' && frames[len(frames)-1].opener == '{')) {
				frames = frames[:len(frames)-1]
			}
			out = append(out, r)
			// closing bracket is significant for outer frame, if any
			setLastSig(r)
			markTopLevelToken()
			continue
		default:
			// whitespace handling
			if unicode.IsSpace(r) {
				// spaces/tabs/newlines are emitted as-is (except newline above)
				out = append(out, r)
				continue
			}
			// significant token
			out = append(out, r)
			setLastSig(r)
			markTopLevelToken()
			continue
		}
	}

	return string(out)
}

// NormalizeMultilineForHistory compacts a possibly-multiline input into a single line
// suitable for history display:
// - Converts CRLF/CR to LF
// - Trims leading/trailing spaces on each line
// - Drops fully empty lines
// - Joins lines with a single space
// It preserves inner token spacing within each original line.
func NormalizeMultilineForHistory(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	joined := strings.Join(out, " ")
	// Compact spaces introduced at bracket boundaries:
	// - conditionally remove spaces immediately after '(', '[', '{'
	// - remove spaces immediately before ')', ']', '}'
	runes := []rune(joined)
	if len(runes) == 0 {
		return joined
	}
	var b []rune
	b = make([]rune, 0, len(runes))
	isSpace := func(r rune) bool { return r == ' ' || r == '\t' }
	isOpener := func(r rune) bool { return r == '(' || r == '[' || r == '{' }
	isCloser := func(r rune) bool { return r == ')' || r == ']' || r == '}' }
	i := 0
	for i < len(runes) {
		r := runes[i]
		// After an opener:
		// - For '[': preserve a single space if next token isn't a bracket/closer
		// - For '(' and '{': drop spaces to compact boundaries
		if isOpener(r) {
			b = append(b, r)
			i++ // move past opener
			// Count spaces after opener
			j := i
			for j < len(runes) && isSpace(runes[j]) {
				j++
			}
			if j < len(runes) {
				n := runes[j]
				if r == '[' {
					// For lists, keep one space before the next non-bracket token
					if isOpener(n) || isCloser(n) {
						i = j
					} else {
						if j > i {
							b = append(b, ' ')
						}
						i = j
					}
				} else {
					// '(' or '{' — compact: drop spaces
					i = j
				}
			}
			continue
		}
		// For a run of spaces/tabs, drop them if the next non-space is a closer
		if isSpace(r) {
			j := i
			for j < len(runes) && isSpace(runes[j]) {
				j++
			}
			if j < len(runes) && isCloser(runes[j]) {
				// skip all spaces before closer
				i = j
				continue
			}
			// preserve current space and continue normally
			b = append(b, r)
			i++
			continue
		}
		// default: copy rune
		b = append(b, r)
		i++
	}
	// Second pass: outside strings, normalize spaces around '=' (but not for '=>')
	outRunes := b
	var c []rune
	c = make([]rune, 0, len(outRunes))
	inString := false
	escape := false
	for i = 0; i < len(outRunes); i++ {
		r := outRunes[i]
		if inString {
			c = append(c, r)
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		if r == '"' {
			inString = true
			c = append(c, r)
			continue
		}
		// If this is '=', and next is not '>', normalize spaces around it
		if r == '=' && (i+1 >= len(outRunes) || outRunes[i+1] != '>') {
			// Trim trailing spaces in c
			for len(c) > 0 {
				last := c[len(c)-1]
				if last == ' ' || last == '\t' {
					c = c[:len(c)-1]
				} else {
					break
				}
			}
			// Ensure single space before '=' if previous isn't start or opener
			if len(c) > 0 && c[len(c)-1] != '(' && c[len(c)-1] != '[' && c[len(c)-1] != '{' && c[len(c)-1] != ' ' {
				c = append(c, ' ')
			}
			// Write '='
			c = append(c, '=')
			// Skip spaces after '=' in source
			j := i + 1
			for j < len(outRunes) && (outRunes[j] == ' ' || outRunes[j] == '\t') {
				j++
			}
			// Add single space after '=' if next isn't closer or end
			if j < len(outRunes) && outRunes[j] != ')' && outRunes[j] != ']' && outRunes[j] != '}' && outRunes[j] != ',' {
				c = append(c, ' ')
			}
			i = j - 1
			continue
		}
		c = append(c, r)
	}
	return string(c)
}
