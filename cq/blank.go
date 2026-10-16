// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"regexp"
	"strings"
)

var (
	backtickStrings = map[string]bool{"go": true, "javascript": true, "typescript": true, "shell": true, "powershell": true}
	noStrings       = map[string]bool{"markdown": true} // prose: apostrophes are not quotes
	charLiterals    = map[string]bool{"rust": true, "c": true, "cpp": true, "java": true, "go": true, "csharp": true,
		"kotlin": true, "scala": true, "swift": true, "dart": true, "zig": true}
	trailingSpace = regexp.MustCompile(`[ \t]+\n`)
)

// BlankComments removes the comments of text in language L, keeping every newline so line
// numbers survive, and returns the blanked code and the comment text. It is string-aware for
// ", ', ` and Python's triple quotes (which it treats as documentation). lostTrack is set when
// the scan ends inside a string or block comment. inString marks, by line from 0, the lines that
// begin inside a string: the body of a multi-line raw string, which is text and not code.
func BlankComments(text string, L *Lang) (blanked, comments string, lostTrack bool, inString []bool) {
	blocks := L.Blocks
	pyTriple := L.Key == "python"
	if pyTriple {
		blocks = nil
	}
	n := len(text)
	var out strings.Builder
	out.Grow(n)
	var cm []string
	inStr := ""
	raw := false
	var strNL []int // offsets of the newlines inside strings
	i := 0
	for i < n {
		c := text[i]
		if inStr != "" {
			if c == '\\' && !raw && i+1 < n {
				if text[i+1] == '\n' {
					strNL = append(strNL, i+1)
				}
				out.WriteByte(c)
				out.WriteByte(text[i+1])
				i += 2
				continue
			}
			if c == '\n' && (inStr == `'` || inStr == `"`) && L.Key != "python" {
				lostTrack = true // unterminated single-line string: resume on the next line
				inStr = ""
				out.WriteByte(c)
				i++
				continue
			}
			if strings.HasPrefix(text[i:], inStr) {
				out.WriteString(inStr)
				i += len(inStr)
				inStr, raw = "", false
				continue
			}
			if c == '\n' {
				strNL = append(strNL, i)
			}
			out.WriteByte(c)
			i++
			continue
		}
		if pyTriple && (strings.HasPrefix(text[i:], `"""`) || strings.HasPrefix(text[i:], `'''`)) {
			q := text[i : i+3]
			j := strings.Index(text[i+3:], q)
			if j < 0 {
				lostTrack = true
				out.WriteString(text[i:])
				break
			}
			seg := text[i : i+3+j+3]
			cm = append(cm, seg)
			out.WriteString(strings.Repeat("\n", strings.Count(seg, "\n")))
			i += len(seg)
			continue
		}
		matched := false
		for _, b := range blocks {
			if strings.HasPrefix(text[i:], b[0]) {
				j := strings.Index(text[i+len(b[0]):], b[1])
				var seg string
				if j < 0 {
					lostTrack = true
					seg = text[i:]
				} else {
					seg = text[i : i+len(b[0])+j+len(b[1])]
				}
				cm = append(cm, seg)
				out.WriteString(strings.Repeat("\n", strings.Count(seg, "\n")))
				i += len(seg)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if L.LineComment != "" && strings.HasPrefix(text[i:], L.LineComment) {
			j := strings.IndexByte(text[i:], '\n')
			if j < 0 {
				j = n - i
			}
			cm = append(cm, text[i:i+j])
			i += j
			continue
		}
		if c == '`' && !backtickStrings[L.Key] || noStrings[L.Key] && (c == '"' || c == '\'') {
			out.WriteByte(c)
			i++
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			if c == '\'' && charLiterals[L.Key] {
				if m := charLiteral(text[i:]); m > 0 {
					out.WriteString(text[i : i+m])
					i += m
					continue
				}
				out.WriteByte(c) // a Rust lifetime ('a) and the like
				i++
				continue
			}
			inStr = string(c)
			raw = c == '`' || L.Key == "python" && i > 0 && (text[i-1] == 'r' || text[i-1] == 'R')
			out.WriteByte(c)
			i++
			continue
		}
		out.WriteByte(c)
		i++
	}
	if inStr != "" {
		lostTrack = true
	}
	inString = make([]bool, strings.Count(text, "\n")+1)
	line, from := 0, 0
	for _, off := range strNL {
		line += strings.Count(text[from:off], "\n")
		from = off
		inString[line+1] = true // the line after this newline begins inside the string
	}
	return trailingSpace.ReplaceAllString(out.String(), "\n"), strings.Join(cm, "\n"), lostTrack, inString
}

// charLiteral returns the length of a character literal ('x', '\n', or two-byte forms) at the
// start of s, or 0.
func charLiteral(s string) int {
	i := 1
	for unit := 0; unit < 2; unit++ {
		if i >= len(s) || s[i] == '\'' || s[i] == '\n' {
			return 0
		}
		if s[i] == '\\' {
			i += 2
		} else {
			i++
		}
		if i < len(s) && s[i] == '\'' {
			return i + 1
		}
	}
	return 0
}

// secretPatterns are replaced by <REDACTED> before any text leaves the machine.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bxox[abp]-[0-9A-Za-z-]{20,}\b`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|passw(or)?d)\b\s*[:=]\s*['"][^'"\s]{12,}['"]`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]{24,}`),
}

// Redact replaces secrets with <REDACTED> and returns how many it replaced.
func Redact(text string) (string, int) {
	count := 0
	for _, p := range secretPatterns {
		text = p.ReplaceAllStringFunc(text, func(string) string { count++; return "<REDACTED>" })
	}
	return text, count
}
