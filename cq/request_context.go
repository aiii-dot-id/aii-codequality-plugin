package cq

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:noinline
func boundedContextWindows(t *Table, lang, source string, size int) ([]SourceWindow, error) {
	return boundedWindowsAt(t, lang, source, size, 1, false)
}

func boundedWindowsAt(t *Table, lang, source string, size, firstLine int, limited bool) ([]SourceWindow, error) {
	return boundedWindowsWith(t, lang, source, size, firstLine, limited, nil)
}

func boundedWindowsWith(t *Table, lang, source string, size, firstLine int, limited bool, units *SegmentUnits) ([]SourceWindow, error) {
	if size <= 0 {
		size = ChunkBytes
	}
	if units == nil && t != nil && t.Langs[lang] != nil && source != "" {
		units = unitsOf(source, lang)
	}
	for {
		windows, err := contextWindowsWith(t, lang, source, size, firstLine, limited, units)
		if err != nil {
			return nil, err
		}
		tooLarge := false
		for _, w := range windows {
			for _, b := range []struct {
				field string
				body  Chunk
				items []Item
			}{{"code", w.Code, CodeItems(lang)}, {"source", w.Context, w.DocItems}} {
				if len(b.items) == 0 {
					continue
				}
				questions := windowQuestions(b.items, b.field, b.body)
				if !requestMayExceed(b.body.Text, questions) {
					continue
				}
				_, err := RequestBody(strings.Repeat("m", 128), map[string]any{"language": lang, b.field: b.body.Text}, questions)
				if errors.Is(err, ErrRequestSize) {
					tooLarge = true
					break
				}
				if err != nil {
					return nil, err
				}
			}
			if tooLarge {
				break
			}
		}
		if !tooLarge {
			return windows, nil
		}
		if size <= 4 {
			return nil, ErrRequestSize
		}
		size = max(4, size/2)
	}
}

func windowQuestions(items []Item, field string, body Chunk) map[string]any {
	questions := Questions(items, field)
	for _, q := range questions {
		fields := q.(map[string]any)
		fields["instructions"] = fmt.Sprintf(questionLines, body.FirstLine, body.LastLine, fields["instructions"])
	}
	return questions
}

func requestMayExceed(text string, questions map[string]any) bool {
	q, err := json.Marshal(questions)
	if err != nil {
		return true
	}
	return 6*len(text)+len(q)+requestRoom >= MaxRequestBytes
}

const requestRoom = 1024
