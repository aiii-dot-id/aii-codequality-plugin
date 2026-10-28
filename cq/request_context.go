package cq

import (
	"errors"
	"fmt"
	"strings"
)

//go:noinline
func boundedContextWindows(t *Table, lang, source string, size int) ([]SourceWindow, error) {
	if size <= 0 {
		size = ChunkBytes
	}
	for {
		windows, err := ContextWindows(t, lang, source, size)
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
				_, err := RequestBody(strings.Repeat("m", 128), map[string]any{"language": lang, b.field: b.body.Text}, windowQuestions(b.items, b.field, b.body))
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
