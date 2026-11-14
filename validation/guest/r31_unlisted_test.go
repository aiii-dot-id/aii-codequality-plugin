package guest_test

import (
	"sort"
	"strings"
	"testing"
)

func TestR31RD04AndST04AreCountedNotListed(t *testing.T) {
	hash := candidateHash(t)
	real, _ := realSources(t)
	names := make([]string, 0, len(real))
	for n := range real {
		names = append(names, n)
	}
	sort.Strings(names)
	name := names[0]
	b := newBroker()
	b.failDocs = false
	b.yes = map[string]bool{"RD-04": true, "ST-04": true}
	b.sources, b.readAttempts, b.submitted = map[string]string{name: real[name]}, map[string]int{}, map[string]int{}
	w := start(t, b, hash)
	defer w.close()
	scanToDone(t, w, b, "r31_unlisted", nil)
	listed := func(entry map[string]any) string {
		var ids []string
		for _, key := range []string{"findings", "slop"} {
			es, _ := entry[key].([]any)
			for _, e := range es {
				ids = append(ids, key+":"+e.(map[string]any)["id"].(string))
			}
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	for _, lane := range []string{"maintainability", "slop"} {
		page, err := reportOf(t, w, object{"scan_id": "r31_unlisted", "shape": "review_queue", "lane": lane})
		if err != nil {
			t.Fatal(err)
		}
		review := page["review"].(map[string]any)
		counted, _ := review["counted_not_listed"].(map[string]any)
		entry := entryOf(review, name)
		t.Logf("%s, default: %s in the queue %v, listed [%s]; counted_not_listed %v", lane, name, entry != nil, listed(entry), counted)

		num := func(v any) float64 { f, _ := v.(float64); return f }
		if strings.Contains(listed(entry), "RD-04") || strings.Contains(listed(entry), "ST-04") || num(counted["RD-04"]) != 1 || num(counted["ST-04"]) != 1 {
			t.Errorf("%s: %s lists RD-04 or ST-04 by default [%s], or they are not each counted once", lane, name, listed(entry))
		}
		page, err = reportOf(t, w, object{"scan_id": "r31_unlisted", "shape": "review_queue", "lane": lane, "list_all": true})
		if err != nil {
			t.Fatal(err)
		}
		review = page["review"].(map[string]any)
		entry = entryOf(review, name)
		t.Logf("%s, list_all: %s listed [%s]; ignored %v", lane, name, listed(entry), page["ignored_arguments"])
		if !strings.Contains(listed(entry), "RD-04") || !strings.Contains(listed(entry), "ST-04") || review["counted_not_listed"] != nil {
			t.Errorf("%s: list_all does not list RD-04 and ST-04", lane)
		}
	}
	summary, err := reportOf(t, w, object{"scan_id": "r31_unlisted", "shape": "summary"})
	if err != nil {
		t.Fatal(err)
	}
	slop := summary["slop"].(map[string]any)
	t.Logf("summary: slop counted_not_listed %v; lead_counts %v", slop["counted_not_listed"], summary["lead_counts"])
	if c, _ := slop["counted_not_listed"].(map[string]any); c == nil || c["RD-04"].(float64) < 1 {
		t.Errorf("the summary does not count them")
	}
}
