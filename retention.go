package main

import (
	"fmt"
	"sort"
	"strings"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type heldScan struct {
	ID        string
	Bytes     int64
	LastWrite int64
	Expires   int64
}

func heldScans(scans []sdk.FileEntry, retentionDays int) []heldScan {
	ids := scanIDsOf(scans)
	owner := map[string]int{}
	for i, id := range ids {
		for _, p := range scanFilePaths(id) {
			owner[strings.TrimPrefix(p, "scans/")] = i
		}
	}
	held := make([]heldScan, len(ids))
	known := make([]bool, len(ids))
	for i, id := range ids {
		held[i].ID = id
		known[i] = true
	}
	for _, e := range scans {
		i, ok := owner[e.Name]
		if !ok {
			continue
		}
		held[i].Bytes += e.Size
		t, ok := parseRFC3339(e.Modified)
		if !ok {
			known[i] = false
			continue
		}
		held[i].LastWrite = max(held[i].LastWrite, t)
	}
	for i := range held {
		if known[i] && held[i].LastWrite > 0 {
			held[i].Expires = held[i].LastWrite + int64(retentionDays)*86400
		} else {
			held[i].LastWrite = 0
		}
	}
	return held
}

func expiredOf(held []heldScan, now int64, except string) []heldScan {
	var out []heldScan
	for _, h := range held {
		if h.Expires > 0 && h.Expires <= now && h.ID != except {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Expires < out[b].Expires })
	return out
}

func removeExpired(f scanFiles, expired []heldScan) ([]any, error) {
	removed := []any{}
	for _, h := range expired {
		if err := removeScanFiles(f, h.ID, true); err != nil {
			return removed, err
		}

		removed = append(removed, map[string]any{"scan_id": h.ID, "finished_at": rfc3339(h.LastWrite), "expires_at": rfc3339(h.Expires), "bytes": h.Bytes})
	}
	return removed, nil
}

const heldWordsMost = 40

func heldWords(held []heldScan) string {
	sorted := append([]heldScan(nil), held...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Expires < sorted[b].Expires })
	var b strings.Builder
	for i, h := range sorted {
		if i == heldWordsMost {
			fmt.Fprintf(&b, scanHeldMore, len(sorted)-i)
			break
		}
		expires := "unknown"
		if h.Expires > 0 {
			expires = rfc3339(h.Expires)
		}
		fmt.Fprintf(&b, scanHeldLine, h.ID, h.Bytes>>10, expires)
	}
	return b.String()
}

func expiryOf(held []heldScan) map[string]string {
	out := map[string]string{}
	for _, h := range held {
		if h.Expires > 0 {
			out[h.ID] = rfc3339(h.Expires)
		}
	}
	return out
}

func parseRFC3339(s string) (int64, bool) {
	if len(s) != 20 || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' || s[19] != 'Z' {
		return 0, false
	}
	num := func(a, b int) (int64, bool) {
		var n int64
		for _, c := range s[a:b] {
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + int64(c-'0')
		}
		return n, true
	}
	y, ok1 := num(0, 4)
	m, ok2 := num(5, 7)
	d, ok3 := num(8, 10)
	hh, ok4 := num(11, 13)
	mm, ok5 := num(14, 16)
	ss, ok6 := num(17, 19)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) || m < 1 || m > 12 || d < 1 || d > 31 || hh > 23 || mm > 59 || ss > 60 {
		return 0, false
	}
	if m <= 2 {
		y--
	}
	era := y / 400
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	days := era*146097 + doe - 719468
	return days*86400 + hh*3600 + mm*60 + ss, true
}

func removeScans(f scanFiles, ids []string) ([]any, error) {
	if len(ids) == 0 {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", scanRemoveNone)
	}
	scans, err := listing(f.List("scans"))
	if err != nil {
		return nil, err
	}
	held := map[string]heldScan{}
	for _, h := range heldScans(scans, 1) {
		held[h.ID] = h
	}
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := held[id]; !ok || !validID(id) {
			missing = append(missing, id)
		}
		if seen[id] {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf(scanRemoveTwice, id))
		}
		seen[id] = true
	}
	if len(missing) > 0 {
		return nil, sdk.Fail("SCAN_UNKNOWN", fmt.Sprintf(scanRemoveUnknown, strings.Join(missing, ", ")))
	}
	removed := []any{}
	for _, id := range ids {
		if err := removeScanFiles(f, id, true); err != nil {
			return removed, err
		}
		removed = append(removed, map[string]any{"scan_id": id, "bytes": held[id].Bytes})
	}
	return removed, nil
}
