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
func RunREPL(session *terraform.ConsoleSession, index *terraform.SymbolIndex, refreshCh <-chan struct{}, scratchDir string) {
	// Setup persistent history file under scratch directory
	cwd, _ := os.Getwd()
	historyPath := filepath.Join(scratchDir, ".terraflow_history")
	// Preload history if exists
	if b, err := os.ReadFile(historyPath); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimRight(ln, "\r")
			if strings.TrimSpace(ln) == "" {
				continue
			}
			// loaded into in-memory history before TTY starts
			// history slice is defined below; we will append after initialization
		}
	}
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
	pendingRefresh := false

	render := func() {
		// CR, clear line, print prompt and buffer, then move cursor back if needed
		os.Stdout.WriteString("\r")
		os.Stdout.WriteString("\x1b[2K") // clear entire line
		os.Stdout.WriteString(prompt)
		os.Stdout.WriteString(string(buf))
		// Move cursor to correct position
		tail := len(buf) - cursor
		if tail > 0 {
			os.Stdout.WriteString(fmt.Sprintf("\x1b[%dD", tail))
		}
	}

	// completion logic inlined in TAB handler

	readKey := make([]byte, 3) // support ESC [ A sequences
	// For Ctrl+C handling
	var lastCtrlC time.Time

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
	go func() {
		for range refreshCh {
			pendingRefresh = true
			// Sync project files to scratch and re-init (no backend file)
			if cwd != "" && scratchDir != "" {
				_ = terraform.SyncToScratch(cwd, scratchDir)
				_ = terraform.InitTerraformInDir(scratchDir)
			}
			// Restart console and rebuild index in the background
			session.Restart()
			if newIdx, err := terraform.BuildSymbolIndex("."); err == nil {
				index = newIdx
			}
			// No user-facing banner; just note internally that a refresh occurred
			refreshNotify <- struct{}{}
		}
	}()

	// Initial render
	render()
	for {
		select {
		case <-refreshNotify:
			// Re-render prompt without spamming the console
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
		case 3: // Ctrl+C
			// First press: try to interrupt terraform console; second press within 1s exits
			now := time.Now()
			if now.Sub(lastCtrlC) < time.Second {
				os.Stdout.WriteString("\r\n[exit]\r\n")
				return
			}
			lastCtrlC = now
			session.Interrupt()
			os.Stdout.WriteString("\r\n[interrupt]\r\n")
			render()
			continue
		case 4: // Ctrl+D
			os.Stdout.WriteString("\r\n[exit]\r\n")
			return
		case '\r', '\n':
			// Submit line
			line := string(buf)
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
				stdout, stderr, evalErr := session.Evaluate(line, 5*time.Second)
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
			if pendingRefresh {
				pendingRefresh = false
			}
			render()
		case 127, 8: // backspace
			if cursor > 0 {
				buf = append(buf[:cursor-1], buf[cursor:]...)
				cursor--
				render()
			}
		case 9: // TAB
			// On TAB, try to complete; if multiple suggestions, insert common prefix
			line := string(buf)
			cands, start, end := index.CompletionCandidates(line, byteOffsetOfRuneIndex(line, cursor))
			if len(cands) == 0 {
				// Rebuild index on-demand in case it's stale/empty
				if newIdx, err := terraform.BuildSymbolIndex("."); err == nil {
					index = newIdx
					// Try again on updated index
					cands, start, end = index.CompletionCandidates(line, byteOffsetOfRuneIndex(line, cursor))
				}
				if len(cands) == 0 {
					// Audible bell to confirm TAB was captured
					os.Stdout.WriteString("\a")
					os.Stdout.WriteString("\r\n(no matches)\r\n")
				}
			} else if len(cands) == 1 {
				prefix := []rune(line[:start])
				suffix := []rune(line[end:])
				replacement := []rune(cands[0])
				buf = append(append(prefix, replacement...), suffix...)
				cursor = len(prefix) + len(replacement)
			} else {
				// Compute common prefix from all candidates
				common := cands[0]
				for _, c := range cands[1:] {
					for len(common) > 0 && (len(c) < len(common) || c[:len(common)] != common) {
						common = common[:len(common)-1]
					}
				}
				if common != "" && common != line[start:end] {
					prefix := []rune(line[:start])
					suffix := []rune(line[end:])
					replacement := []rune(common)
					buf = append(append(prefix, replacement...), suffix...)
					cursor = len(prefix) + len(replacement)
				} else {
					os.Stdout.WriteString("\r\n")
					os.Stdout.WriteString(strings.Join(cands, "  "))
					os.Stdout.WriteString("\r\n")
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
					}
				case 'D': // left
					if cursor > 0 {
						cursor--
					}
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
