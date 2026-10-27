package cq

import (
	"crypto/sha256"
	"encoding/json"
)

func JournalReport(j *Journal, maxBytes int) (records []Record, coverage []Coverage, digest []byte, complete bool, err error) {
	h := sha256.New()
	used := 0
	complete = true
	_, err = j.Walk(0, func(e JournalEvent) error {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		h.Write(b)
		h.Write([]byte{'\n'})
		if !complete || len(b) > maxBytes-used {
			complete = false
			return nil
		}
		used += len(b)
		if e.Record != nil {
			records = append(records, *e.Record)
		} else {
			coverage = append(coverage, *e.Coverage)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, false, err
	}
	return DeduplicateRecords(records), coverage, h.Sum(nil), complete, nil
}
