package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flowave-io/terraflow/internal/terraform"
)

// RunREPL starts the interactive console loop with history and autocompletion.
// Uses raw TTY on Unix to capture TAB and arrows; gracefully degrades otherwise.
// scratchDir is the working directory used by terraform console (e.g., .terraflow).
func RunREPL(session *terraform.ConsoleSession, index *terraform.SymbolIndex, refreshCh <-chan struct{}, scratchDir string, varFiles []string) {
	// Setup persistent history file under scratch directory
	cwd, _ := os.Getwd()
	historyPath := filepath.Join(scratchDir, ".terraflow_history")
	tty, restore, _ := acquireTTY()
	if restore != nil {
		defer restore()
	}
	if tty != nil {
		defer tty.Close()
	}

	const prompt = ">> "
	buf := []rune{}
	cursor := 0
	history := []string{}
	// Re-read file now and append to history (after slice is created)
	if b, err := os.ReadFile(historyPath); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimRight(ln, "\r")
			if strings.TrimSpace(ln) == "" {
				continue
			}
			history = append(history, ln)
		}
	}
	// Open file for appending executed commands
	historyFile, _ := os.OpenFile(historyPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if historyFile != nil {
		defer historyFile.Close()
	}
	histIdx := -1 // -1 means not navigating
	// TAB-cycle state
	lastTabCands := []string{}
	lastTabStart, lastTabEnd := 0, 0
	lastTabIdx := -1
	lastTabPrefix := ""
	lastTabSuffix := ""
	lastTabListRows := 0
	// After accepting a suggestion, hide ghost until next user input
	suppressGhostUntilInput := false
	// cached ghost suggestion (history-based)
	ghostCache := ""
	// minimal ANSI styling support. Ghost = dim; highlight = also dim per request.
	const ansiDim = "\x1b[2m"
	const ansiReset = "\x1b[0m"
	const ansiGhost = ansiDim
	pendingRefresh := false

	// Best history suggestion for the current full-line prefix
	bestHistorySuggestion := func(prefix string) string {
		if len(history) == 0 {
			return ""
		}
		// Only suggest when cursor is at end of line to avoid mid-line confusion
		if cursor != len(buf) {
			return ""
		}
		if strings.TrimSpace(prefix) == "" {
			return ""
		}
		// Scan MRU (latest first)
		for i := len(history) - 1; i >= 0; i-- {
			h := history[i]
			if strings.HasPrefix(h, prefix) && h != prefix {
				return h[len(prefix):]
			}
		}
		return ""
	}

	// History candidates are no longer merged into TAB completion. We keep only index-based TAB suggestions.

	render := func() {
		// CR, clear line, print prompt and buffer, then move cursor back if needed
		os.Stdout.WriteString("\r")
		os.Stdout.WriteString("\x1b[2K") // clear entire line
		os.Stdout.WriteString(prompt)
		line := string(buf)
		os.Stdout.WriteString(line)
		// Inline ghost suggestion from selection or history (dim)
		ghost := ""
		if !suppressGhostUntilInput && lastTabIdx >= 0 && len(lastTabCands) > 0 {
			// Build ghost from currently selected candidate if it extends the current token
			sel := lastTabCands[lastTabIdx]
			// Compute current token text from up-to-date line
			if lastTabStart >= 0 && lastTabStart <= len(line) && lastTabEnd >= lastTabStart && lastTabEnd <= len(line) {
				tok := line[lastTabStart:lastTabEnd]
				if strings.HasPrefix(sel, tok) && len(sel) > len(tok) {
					ghost = sel[len(tok):]
				}
			}
		}
		// Function ghost suggestion (only when not cycling TAB and at EOL)
		if !suppressGhostUntilInput && ghost == "" && lastTabIdx < 0 && cursor == len(buf) && len(index.Functions) > 0 {
			// Determine the current bare identifier token (letters/digits/underscore only)
			i := len(line)
			start := i
			for start > 0 {
				r := rune(line[start-1])
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
					start--
					continue
				}
				break
			}
			tok := line[start:i]
			if tok != "" {
				// Avoid suggesting inside attribute chains like module.x.abc
				if start == 0 || line[start-1] != '.' {
					lt := strings.ToLower(tok)
					for _, fn := range index.Functions {
						if strings.HasPrefix(fn, lt) {
							if fn == lt {
								ghost = "("
							} else {
								ghost = fn[len(lt):] + "("
							}
							break
						}
					}
				}
			}
		}
		if !suppressGhostUntilInput && ghost == "" {
			ghost = bestHistorySuggestion(line)
		}
		ghostCache = ghost
		if ghost != "" {
			os.Stdout.WriteString(ansiGhost)
			os.Stdout.WriteString(ghost)
			os.Stdout.WriteString(ansiReset)
		}
		// Move cursor back over any ghost and the tail from mid-line edits
		// First account for ghost length if cursor is not at end
		back := 0
		if ghost != "" {
			back += len(ghost)
		}
		tail := len(buf) - cursor
		back += tail
		if back > 0 {
			os.Stdout.WriteString(fmt.Sprintf("\x1b[%dD", back))
		}
	}

	// Helper: clear any printed suggestion list below the prompt
	clearSuggestionList := func() {
		if lastTabListRows > 0 {
			// Move to first overlay line below the prompt
			os.Stdout.WriteString("\x1b[1B")
			for r := 0; r < lastTabListRows; r++ {
				// Clear line
				os.Stdout.WriteString("\r\x1b[2K")
				// Move down to next overlay line except after the last one
				if r < lastTabListRows-1 {
					os.Stdout.WriteString("\x1b[1B")
				}
			}
			// Return cursor to the prompt line
			os.Stdout.WriteString(fmt.Sprintf("\x1b[%dA", lastTabListRows))
			lastTabListRows = 0
		}
	}

	// Helpers to draw candidate lists with highlighting of the selected index
	// For lists: non-selected items use ghost (dim), selected item uses normal text
	// removed: ansiRev
	// removed: printCandidatesFresh (we always overwrite in place)

	printCandidatesOverwrite := func(cands []string, selected int, prevRows int) int {
		w := detectTermWidth(tty)
		if w <= 0 {
			w = 80
		}
		maxLen := 0
		for _, s := range cands {
			if l := len(s); l > maxLen {
				maxLen = l
			}
		}
		pad := 2
		colW := maxLen + pad
		if colW <= 0 {
			colW = 10
		}
		cols := w / colW
		var rows int
		if cols <= 1 {
			rows = len(cands)
		} else {
			rows = (len(cands) + cols - 1) / cols
		}
		// Ensure there are dedicated overlay lines below the prompt.
		// If this is the first draw, allocate `rows` new lines so we don't overwrite prior output.
		if prevRows == 0 {
			for i := 0; i < rows; i++ {
				os.Stdout.WriteString("\r\n")
			}
			// Return cursor to the prompt line
			os.Stdout.WriteString(fmt.Sprintf("\x1b[%dA", rows))
		} else if rows > prevRows {
			// Allocate extra lines if the overlay grew
			delta := rows - prevRows
			for i := 0; i < delta; i++ {
				os.Stdout.WriteString("\r\n")
			}
			// Return cursor to the prompt line
			os.Stdout.WriteString(fmt.Sprintf("\x1b[%dA", delta))
		}
		// Move to first list line; render overlay directly below prompt
		os.Stdout.WriteString("\x1b[1B")
		// Overwrite max(prevRows, rows) lines
		total := prevRows
		if rows > total {
			total = rows
		}
		if total == 0 {
			total = rows
		}
		for r := 0; r < total; r++ {
			// Clear line
			os.Stdout.WriteString("\r\x1b[2K")
			if r < rows {
				// Compose row r
				for c := 0; c < cols; c++ {
					idx := r + c*rows
					if idx >= len(cands) {
						break
					}
					s := cands[idx]
					if idx != selected {
						os.Stdout.WriteString(ansiGhost)
					}
					os.Stdout.WriteString(s)
					if idx != selected {
						os.Stdout.WriteString(ansiReset)
					}
					if c < cols-1 {
						if sp := colW - len(s); sp > 0 {
							os.Stdout.WriteString(strings.Repeat(" ", sp))
						}
					}
				}
			}
			if r < total-1 {
				os.Stdout.WriteString("\x1b[1B")
			}
		}
		// Move back up to the prompt line
		os.Stdout.WriteString(fmt.Sprintf("\x1b[%dA", total))
		return rows
	}

	// completion logic inlined in TAB handler

	readKey := make([]byte, 3) // support ESC [ A sequences

	// Ensure newlines render correctly in raw TTY: map lone \n to \r\n
	normalizeTTYNewlines := func(s string) string {
		if s == "" {
			return s
		}
		var b strings.Builder
		b.Grow(len(s) + len(s)/8)
		prev := byte(0)
		for i := 0; i < len(s); i++ {
			ch := s[i]
			if ch == '\n' {
				if prev != '\r' {
					b.WriteString("\r\n")
				} else {
					b.WriteByte('\n')
				}
			} else {
				b.WriteByte(ch)
			}
			prev = ch
		}
		return b.String()
	}

	// Non-blocking refresh watcher
	refreshNotify := make(chan struct{}, 1)
	lastScan := time.Now()
	go func() {
		for range refreshCh {
			pendingRefresh = true
			changedTFOnly := false
			// Sync project files to scratch and re-init (no backend file)
			if cwd != "" && scratchDir != "" {
				changed, changedTF, _ := terraform.SyncToScratch(cwd, scratchDir)
				if !changed {
					// Nothing to do
					pendingRefresh = false
					continue
				}
				// Track whether only tfvars/json changed (no .tf)
				changedTFOnly = !changedTF
				// Fast-path: literal-only patch is instant
				statePath := filepath.Join(scratchDir, "terraform.tfstate")
				_ = terraform.PatchStateFromConfigLiterals(scratchDir, statePath)
				// Target only files changed since last scan for non-literals
				changedFiles := []string{}
				filepath.Walk(scratchDir, func(p string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return nil
					}
					if strings.ToLower(filepath.Ext(p)) != ".tf" {
						return nil
					}
					if info.ModTime().After(lastScan) {
						changedFiles = append(changedFiles, p)
					}
					return nil
				})
				if len(changedFiles) > 0 {
					// For each changed resource block/attribute, run the exact same targeted logic
					// by calling the exact attribute patch for type+name+attr
					_ = terraform.PatchTargetedExactByFiles(scratchDir, scratchDir, statePath, varFiles, changedFiles)
				}
				lastScan = time.Now()
			}
			// Restart console and rebuild index in the background
			session.Restart()
			// Only rebuild index if structural .tf files changed; tfvars-only changes
			// should not impact completion. This reduces refresh cost.
			if !changedTFOnly {
				// Rebuild index from project root to include all locals/modules even if some files are skipped in scratch
				if newIdx, err := terraform.BuildSymbolIndex(cwd); err == nil {
					index = newIdx
				}
			}
			// No user-facing banner; just note internally that a refresh occurred
			refreshNotify <- struct{}{}
		}
	}()

	// Initial render
	render()

	// Background warm-up evaluation to prime terraform and OS caches
	go func() {
		_, _, _ = session.Evaluate("0", 10*time.Second)
	}()
	for {
		select {
		case <-refreshNotify:
			// Clear any overlay and re-render prompt without spamming the console
			clearSuggestionList()
			render()
			continue
		default:
		}

		// Read a single byte first
		n, err := tty.Read(readKey[:1])
		if err != nil || n == 0 {
			os.Stdout.WriteString("\r\n")
			return
		}
		b := readKey[0]
		switch b {
		case 3: // Ctrl+C — behave like Bash: clear current input and show a fresh prompt
			os.Stdout.WriteString("\r\n")
			buf = buf[:0]
			cursor = 0
			histIdx = -1
			render()
			continue
		case 4: // Ctrl+D
			os.Stdout.WriteString("\r\n[exit]\r\n")
			return
		case '\r', '\n':
			// If TAB cycle is active, ENTER accepts the selected suggestion (no execute).
			// Otherwise, if a ghost suggestion exists at EOL, accept it (no execute).
			curLine := string(buf)
			cycleActive := lastTabIdx >= 0 && strings.HasPrefix(curLine, lastTabPrefix) && strings.HasSuffix(curLine, lastTabSuffix)
			if cycleActive && len(lastTabCands) > 0 {
				// Accept currently selected TAB suggestion instead of executing
				sel := lastTabCands[lastTabIdx]
				p := []rune(lastTabPrefix)
				s := []rune(lastTabSuffix)
				r := []rune(sel)
				buf = append(append(p, r...), s...)
				cursor = len(p) + len(r)
				clearSuggestionList()
				// Reset cycle state to avoid stale ghosts
				lastTabCands = nil
				lastTabIdx = -1
				lastTabPrefix = ""
				lastTabSuffix = ""
				lastTabStart, lastTabEnd = 0, 0
				suppressGhostUntilInput = true
				render()
				continue
			}
			if cursor == len(buf) && ghostCache != "" {
				// Accept ghost suggestion instead of executing
				ins := []rune(ghostCache)
				buf = append(buf, ins...)
				cursor = len(buf)
				ghostCache = ""
				clearSuggestionList()
				// Reset cycle state
				lastTabCands = nil
				lastTabIdx = -1
				lastTabPrefix = ""
				lastTabSuffix = ""
				lastTabStart, lastTabEnd = 0, 0
				suppressGhostUntilInput = true
				render()
				continue
			}
			// Submit line
			line := string(buf)
			// Clear overlay before printing a new line
			clearSuggestionList()
			os.Stdout.WriteString("\r\n")
			if strings.TrimSpace(line) != "" {
				if line == "exit" || line == "quit" {
					return
				}
				// Only record if not a consecutive duplicate
				if len(history) == 0 || history[len(history)-1] != line {
					history = append(history, line)
					// Persist command into history file
					if historyFile != nil {
						_, _ = historyFile.WriteString(line + "\n")
					}
				}
				// Always reset navigation
				histIdx = -1
				stdout, stderr, evalErr := session.Evaluate(line, 15*time.Second)
				if stdout != "" {
					os.Stdout.WriteString(normalizeTTYNewlines(stdout))
					if !strings.HasSuffix(stdout, "\n") && !strings.HasSuffix(stdout, "\r\n") {
						os.Stdout.WriteString("\r\n")
					}
				}
				if stderr != "" {
					os.Stderr.WriteString(normalizeTTYNewlines(stderr))
					if !strings.HasSuffix(stderr, "\n") && !strings.HasSuffix(stderr, "\r\n") {
						os.Stderr.WriteString("\r\n")
					}
				}
				if evalErr != nil {
					msg := evalErr.Error()
					if msg != "" {
						os.Stderr.WriteString(normalizeTTYNewlines(msg))
						if !strings.HasSuffix(msg, "\n") && !strings.HasSuffix(msg, "\r\n") {
							os.Stderr.WriteString("\r\n")
						}
					}
				}
			}
			buf = buf[:0]
			cursor = 0
			// reset TAB cycle and ghost
			lastTabCands = nil
			lastTabIdx = -1
			// lastTabInput removed
			ghostCache = ""
			if pendingRefresh {
				pendingRefresh = false
			}
			render()
		case 127, 8: // backspace
			if cursor > 0 {
				buf = append(buf[:cursor-1], buf[cursor:]...)
				cursor--
				// any edit cancels TAB cycle
				lastTabCands = nil
				lastTabIdx = -1
				// lastTabInput removed
				clearSuggestionList()
				render()
			}
		case 9: // TAB
			// User is actively requesting suggestions again; allow ghost
			suppressGhostUntilInput = false
			line := string(buf)
			// TAB should not accept or suggest history; prefer index candidates over function ghosts.
			// Determine cycle state and current index candidates.
			cycleActive := lastTabIdx >= 0 && strings.HasPrefix(line, lastTabPrefix) && strings.HasSuffix(line, lastTabSuffix)

			var cands []string
			var start, end int
			if cycleActive && len(lastTabCands) > 0 {
				// Reuse previous candidate set and token bounds so TAB truly cycles
				cands = lastTabCands
				start, end = lastTabStart, lastTabEnd
			} else {
				cands, start, end = index.CompletionCandidates(line, byteOffsetOfRuneIndex(line, cursor))
				// Do not trigger a synchronous index rebuild on TAB; return fast for UX responsiveness
			}

			// If not cycling and at EOL, only accept a function ghost when there are no index candidates.
			if !cycleActive && cursor == len(buf) && len(index.Functions) > 0 && len(cands) == 0 {
				// Recompute function ghost like in render(), to avoid accepting history ghosts
				i := len(line)
				startTok := i
				for startTok > 0 {
					r := rune(line[startTok-1])
					if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
						startTok--
						continue
					}
					break
				}
				tok := line[startTok:i]
				fghost := ""
				if tok != "" && (startTok == 0 || line[startTok-1] != '.') {
					lt := strings.ToLower(tok)
					for _, fn := range index.Functions {
						if strings.HasPrefix(fn, lt) {
							if fn == lt {
								fghost = "("
							} else {
								fghost = fn[len(lt):] + "("
							}
							break
						}
					}
				}
				if fghost != "" {
					ins := []rune(fghost)
					buf = append(buf, ins...)
					cursor = len(buf)
					// Do not start a cycle; keep functions out of lists
					lastTabCands = nil
					lastTabIdx = -1
					lastTabPrefix = ""
					lastTabSuffix = ""
					lastTabStart, lastTabEnd = 0, 0
					clearSuggestionList()
					suppressGhostUntilInput = true
					render()
					continue
				}
			}

			if len(cands) == 0 {
				// No matches; return quickly and silently
				os.Stdout.WriteString("\a")
				render()
				continue
			}

			// If first time or context changed, initialize cycle state
			if !cycleActive {
				// Initialize cycle and insert the common prefix shared by all candidates (if any)
				lastTabCands = cands
				// Compute longest common prefix among candidates
				common := cands[0]
				for _, c := range cands[1:] {
					for len(common) > 0 && (len(c) < len(common) || c[:len(common)] != common) {
						common = common[:len(common)-1]
					}
				}
				// Current token text
				tok := line[start:end]
				prefixStr := line[:start]
				suffixStr := line[end:]
				if common != "" && common != tok {
					// Replace token with common prefix
					pRunes := []rune(prefixStr)
					rRunes := []rune(common)
					sRunes := []rune(suffixStr)
					buf = append(append(pRunes, rRunes...), sRunes...)
					cursor = len(pRunes) + len(rRunes)
					// Update token bounds after insertion
					lastTabStart = len(prefixStr)
					lastTabEnd = lastTabStart + len(common)
					lastTabPrefix = prefixStr
					lastTabSuffix = suffixStr
				} else {
					// No extra commonality; keep bounds as-is
					lastTabStart, lastTabEnd = start, end
					lastTabPrefix = prefixStr
					lastTabSuffix = suffixStr
				}
				lastTabIdx = 0
			} else {
				// advance cycle
				lastTabIdx++
				if lastTabIdx >= len(lastTabCands) {
					lastTabIdx = 0
				}
			}
			// Keep buffer unchanged and only render ghost for all levels
			sel := lastTabCands[lastTabIdx]
			_ = sel
			// keep lastTabInput removed; we now track stability via prefix/suffix containment
			// Draw list unless we're at attribute level (type.name.attr*), where list should be hidden
			if len(lastTabCands) > 0 {
				attrLevel := strings.Count(sel, ".") >= 2
				if attrLevel {
					clearSuggestionList()
				} else if len(lastTabCands) > 1 {
					// Draw suggestions on a virtual overlay line without moving the prompt
					lastTabListRows = printCandidatesOverwrite(lastTabCands, lastTabIdx, lastTabListRows)
				}
			}
			render()
		case 27: // ESC sequence
			// Read next two bytes for CSI if available
			nn, _ := tty.Read(readKey[1:3])
			if nn >= 2 && readKey[1] == '[' {
				switch readKey[2] {
				case 'C': // right
					if cursor < len(buf) {
						cursor++
						lastTabCands = nil
						lastTabIdx = -1
						clearSuggestionList()
					} else if ghostCache != "" {
						// Accept ghost suggestion at EOL
						ins := []rune(ghostCache)
						buf = append(buf, ins...)
						cursor = len(buf)
						ghostCache = ""
						// Clear any visible list once ghost is accepted
						clearSuggestionList()
						// Reset cycle state to avoid stale ghosts
						lastTabCands = nil
						lastTabIdx = -1
						lastTabPrefix = ""
						lastTabSuffix = ""
						lastTabStart, lastTabEnd = 0, 0
						suppressGhostUntilInput = true
					} else if lastTabIdx >= 0 && len(lastTabCands) > 0 {
						// Accept currently selected suggestion even if ghost is hidden (e.g., attribute level)
						line := string(buf)
						_ = line
						sel := lastTabCands[lastTabIdx]
						p := []rune(lastTabPrefix)
						s := []rune(lastTabSuffix)
						r := []rune(sel)
						buf = append(append(p, r...), s...)
						cursor = len(p) + len(r)
						clearSuggestionList()
						// Reset cycle state to avoid stale ghosts
						lastTabCands = nil
						lastTabIdx = -1
						lastTabPrefix = ""
						lastTabSuffix = ""
						lastTabStart, lastTabEnd = 0, 0
						suppressGhostUntilInput = true
					}
				case 'D': // left
					if cursor > 0 {
						cursor--
					}
					clearSuggestionList()
				case 'A': // up - history prev
					if len(history) > 0 {
						if histIdx == -1 {
							histIdx = len(history)
						}
						if histIdx > 0 {
							histIdx--
						}
						buf = []rune(history[histIdx])
						cursor = len(buf)
					}
				case 'B': // down - history next
					if histIdx >= 0 {
						histIdx++
						if histIdx >= len(history) {
							histIdx = -1
							buf = buf[:0]
						} else {
							buf = []rune(history[histIdx])
						}
						cursor = len(buf)
					}
				case 'Z': // Shift+TAB (reverse cycle)
					// Mirror TAB behavior but cycle backward. Do not modify the buffer
					// (other than inserting a common prefix on first activation).
					suppressGhostUntilInput = false
					line := string(buf)
					cycleActive := lastTabIdx >= 0 && strings.HasPrefix(line, lastTabPrefix) && strings.HasSuffix(line, lastTabSuffix)

					var cands []string
					var start, end int
					if cycleActive && len(lastTabCands) > 0 {
						cands = lastTabCands
						start, end = lastTabStart, lastTabEnd
					} else {
						cands, start, end = index.CompletionCandidates(line, byteOffsetOfRuneIndex(line, cursor))
						if len(cands) == 0 {
							os.Stdout.WriteString("\a")
							render()
							break
						}
						// Initialize cycle and optionally insert common prefix among candidates
						lastTabCands = cands
						common := cands[0]
						for _, c := range cands[1:] {
							for len(common) > 0 && (len(c) < len(common) || c[:len(common)] != common) {
								common = common[:len(common)-1]
							}
						}
						tok := line[start:end]
						prefixStr := line[:start]
						suffixStr := line[end:]
						if common != "" && common != tok {
							pRunes := []rune(prefixStr)
							rRunes := []rune(common)
							sRunes := []rune(suffixStr)
							buf = append(append(pRunes, rRunes...), sRunes...)
							cursor = len(pRunes) + len(rRunes)
							lastTabStart = len(prefixStr)
							lastTabEnd = lastTabStart + len(common)
							lastTabPrefix = prefixStr
							lastTabSuffix = suffixStr
						} else {
							lastTabStart, lastTabEnd = start, end
							lastTabPrefix = prefixStr
							lastTabSuffix = suffixStr
						}
						// Start from the last candidate for reverse cycling
						lastTabIdx = len(lastTabCands) - 1
					}

					if cycleActive && len(lastTabCands) > 0 {
						// Move backward in the cycle
						lastTabIdx--
						if lastTabIdx < 0 {
							lastTabIdx = len(lastTabCands) - 1
						}
					}

					// Draw list overlay similar to TAB without inserting selection
					if len(lastTabCands) > 0 {
						sel := lastTabCands[lastTabIdx]
						attrLevel := strings.Count(sel, ".") >= 2
						if attrLevel {
							clearSuggestionList()
						} else if len(lastTabCands) > 1 {
							lastTabListRows = printCandidatesOverwrite(lastTabCands, lastTabIdx, lastTabListRows)
						}
					}
					render()
				}
				render()
			}
		default:
			// Printable characters
			if b >= 32 && b <= 126 {
				// insert
				r := rune(b)
				buf = append(buf[:cursor], append([]rune{r}, buf[cursor:]...)...)
				cursor++
				// any edit cancels TAB cycle
				lastTabCands = nil
				lastTabIdx = -1
				clearSuggestionList()
				suppressGhostUntilInput = false
				render()
			}
		}
	}
}

func byteOffsetOfRuneIndex(s string, runeIndex int) int {
	// Since we only insert ASCII runes above, this is safe/simple.
	if runeIndex < 0 {
		return 0
	}
	if runeIndex > len(s) {
		return len(s)
	}
	return runeIndex
}
