package cq

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

var errPrefixRanked = errors.New("report prefix ranked")

func JournalReport(j *Journal, maxBytes int, committed int64) (records []Record, coverage []Coverage, digest []byte, complete bool, err error) {
	h := sha256.New()
	used := 0
	complete = true
	end, err := j.Walk(0, func(e JournalEvent) error {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if len(b) > maxBytes-used {
			complete = false
			return errPrefixRanked
		}
		used += len(b)
		h.Write(b)
		h.Write([]byte{'\n'})
		if e.Record != nil {
			records = append(records, *e.Record)
		} else {
			coverage = append(coverage, *e.Coverage)
		}
		return nil
	})
	if errors.Is(err, errPrefixRanked) {
		end, err = j.Walk(max(end, committed), nil)
	}
	if err != nil {
		return nil, nil, nil, false, err
	}
	fmt.Fprintf(h, "end:%d", end)
	return DeduplicateRecords(records), coverage, h.Sum(nil), complete, nil
}
