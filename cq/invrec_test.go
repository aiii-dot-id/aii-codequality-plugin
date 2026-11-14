package cq

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func invTree() []InvRec {
	root := InvRec{Kind: InvDir, Index: 1, Path: ".", State: InvListed, Entries: 3,
		List: InvListDigest([]InvEntry{{"a.go", InvFile}, {"b.go", InvFile}, {"sub", InvDir}})}
	a := InvRec{Kind: InvFile, Index: 2, Parent: 1, Path: "a.go", State: "mapped", Size: 10, Digest: "da"}
	b := InvRec{Kind: InvFile, Index: 3, Parent: 1, Path: "b.go", State: "mapped", Size: 20, Digest: "db"}
	sub := InvRec{Kind: InvDir, Index: 4, Parent: 1, Path: "sub", State: InvListed, Entries: 1,
		List: InvListDigest([]InvEntry{{"x.go", InvFile}})}
	x := InvRec{Kind: InvFile, Index: 5, Parent: 4, Path: "sub/x.go", State: "mapped", Size: 30, Digest: "dx"}
	members := []InvRec{root, a, b, sub, x}
	return append(members, InvRec{Kind: InvDone, Total: 5, Closure: InvClosure(members)})
}

func invStore(t *testing.T, recs []InvRec, tear int) *recMem {
	t.Helper()
	m := &recMem{}
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		line := recMust(t, string(b))
		if r.Kind != InvDone && r.Index == tear {
			line[len(line)/2] ^= 1
		}
		m.Append("inv", line)
	}
	return m
}

func invRead(t *testing.T, recs []InvRec, tear int) (*InvView, RecStats) {
	t.Helper()
	v, st, err := ReadInventory(invStore(t, recs, tear), "inv")
	if err != nil {
		t.Fatal(err)
	}
	return v, st
}

func TestInvRecAWholeTreeCloses(t *testing.T) {
	v, st := invRead(t, invTree(), 0)
	if v.Status != InvComplete || v.Total != 5 || v.Unknown != 0 || st.Torn != 0 {
		t.Fatalf("status %s, total %d, unknown %d, torn %d, why %v", v.Status, v.Total, v.Unknown, st.Torn, v.Why)
	}
	if !v.Established(1) || !v.Established(4) {
		t.Fatalf("root established %v, sub established %v, want both", v.Established(1), v.Established(4))
	}
}

func TestInvRecWithoutItsClosingRecordConcludesNothing(t *testing.T) {
	v, _ := invRead(t, invTree()[:5], 0)
	if v.Status != InvUnfinished {
		t.Fatalf("status %s, want unfinished", v.Status)
	}
	if v.Established(1) || v.Established(4) {
		t.Fatal("a directory is established before the inventory has closed")
	}
}

func TestInvRecADamagedMemberUnderAVerifiedFooterIsDamaged(t *testing.T) {
	v, st := invRead(t, invTree(), 3)
	if st.Torn != 1 {
		t.Fatalf("%d torn, want 1", st.Torn)
	}
	if v.Status != InvDamaged {
		t.Fatalf("status %s, want damaged: the closing record was believed without its members", v.Status)
	}
	if fmt.Sprint(v.Missing) != "[3]" || v.Total != 5 || v.Unknown != 1 {
		t.Fatalf("missing %v, total %d, unknown %d, want [3], 5, 1", v.Missing, v.Total, v.Unknown)
	}
	if v.Established(1) {
		t.Fatal("the root is established with a member missing: its package would get an unused-function conclusion")
	}
	if !v.Established(4) {
		t.Fatal("sub is whole and its directories are recorded, and it is not established")
	}
}

func TestInvRecALostDirectoryKeepsItsDescendantsUnknown(t *testing.T) {
	v, _ := invRead(t, invTree(), 4)
	if v.Status != InvDamaged || fmt.Sprint(v.Missing) != "[4]" {
		t.Fatalf("status %s, missing %v, want damaged and [4]", v.Status, v.Missing)
	}
	if v.Total != 5 {
		t.Fatalf("total %d, want 5: the denominator moved", v.Total)
	}
	if v.Unknown != 2 {
		t.Fatalf("unknown %d, want 2: sub and the file under it", v.Unknown)
	}
	if v.Established(4) || v.Established(1) {
		t.Fatal("a directory is established with sub's record lost")
	}
}

func TestInvRecAnAlteredMemberBreaksTheFooter(t *testing.T) {
	recs := invTree()
	recs[1].Digest = "zz"
	v, _ := invRead(t, recs, 0)
	if v.Status != InvDamaged {
		t.Fatalf("status %s, want damaged: a member differs from what the closing record was made over", v.Status)
	}
	if len(v.Why) != 1 || !strings.Contains(v.Why[0], "digest") {
		t.Fatalf("why %v, want the digest alone", v.Why)
	}
}

func TestInvRecARenamedMemberIsNotAMembershipList(t *testing.T) {
	recs := invTree()
	recs[2].Path = "c.go"
	v, _ := invRead(t, recs, 0)
	if v.Status != InvDamaged {
		t.Fatalf("status %s, want damaged", v.Status)
	}
	if v.Established(1) {
		t.Fatal("the root is established though its records are not the entries its listing returned")
	}
	if !v.Established(4) {
		t.Fatal("sub is whole and is not established")
	}
}

func TestInvRecAMemberWrittenBeforeItsDirectoryIsUnknown(t *testing.T) {
	r := invTree()
	v, _ := invRead(t, []InvRec{r[0], r[4], r[1], r[2], r[3], r[5]}, 0)
	if v.Status != InvDamaged || v.Unknown != 1 || v.Orphans != 1 {
		t.Fatalf("status %s, unknown %d, orphans %d, want damaged, 1, 1", v.Status, v.Unknown, v.Orphans)
	}
	if v.Established(4) {
		t.Fatal("sub is established though its entry was written before it")
	}
}

func TestInvRecTheFirstRecordForANumberWins(t *testing.T) {
	r := invTree()
	late := r[1]
	late.Digest = "other"
	v, _ := invRead(t, []InvRec{r[0], r[1], late, r[2], r[3], r[4], r[5]}, 0)
	if v.Status != InvComplete || v.Conflicts != 1 || v.Members != 5 {
		t.Fatalf("status %s, conflicts %d, members %d, why %v, want complete, 1, 5", v.Status, v.Conflicts, v.Members, v.Why)
	}
	v, _ = invRead(t, []InvRec{r[0], late, r[1], r[2], r[3], r[4], r[5]}, 0)
	if v.Status != InvDamaged || v.Conflicts != 1 {
		t.Fatalf("the other record first: status %s, conflicts %d, want damaged and 1", v.Status, v.Conflicts)
	}
}

func TestInvRecAMemberAboveTheTotalIsOutsideIt(t *testing.T) {
	r := invTree()
	extra := InvRec{Kind: InvFile, Index: 6, Parent: 4, Path: "sub/y.go", State: "mapped", Size: 5, Digest: "dy"}
	for name, recs := range map[string][]InvRec{
		"before the closing record": {r[0], r[1], r[2], r[3], r[4], extra, r[5]},
		"after the closing record":  {r[0], r[1], r[2], r[3], r[4], r[5], extra},
	} {
		v, _ := invRead(t, recs, 0)
		if v.Status != InvDamaged || v.Total != 5 || v.Unknown != 0 || v.Orphans != 1 {
			t.Fatalf("%s: status %s, total %d, unknown %d, orphans %d, want damaged, 5, 0, 1", name, v.Status, v.Total, v.Unknown, v.Orphans)
		}
		if v.Established(4) {
			t.Fatalf("%s: sub is established with a record its listing did not return", name)
		}
		if !v.Established(1) {
			t.Fatalf("%s: the root is whole and is not established", name)
		}
	}
}

func TestInvRecASecondClosingRecordIsCountedAndTheFirstStands(t *testing.T) {
	r := invTree()
	other := InvRec{Kind: InvDone, Total: 4, Closure: InvClosure(r[:4])}
	v, _ := invRead(t, []InvRec{r[0], r[1], r[2], r[3], r[4], r[5], other}, 0)
	if v.Status != InvComplete || v.Total != 5 || v.Conflicts != 1 {
		t.Fatalf("a different one after: status %s, total %d, conflicts %d, why %v, want complete, 5, 1", v.Status, v.Total, v.Conflicts, v.Why)
	}
	v, _ = invRead(t, []InvRec{r[0], r[1], r[2], r[3], r[4], r[5], r[5]}, 0)
	if v.Status != InvComplete || v.Conflicts != 0 {
		t.Fatalf("the same one again: status %s, conflicts %d, want complete and 0", v.Status, v.Conflicts)
	}
	v, _ = invRead(t, []InvRec{r[0], r[1], r[2], r[3], other, r[4], r[5]}, 0)
	if v.Status != InvDamaged || v.Total != 4 || v.Conflicts != 1 {
		t.Fatalf("the other one first: status %s, total %d, conflicts %d, want damaged, 4, 1", v.Status, v.Total, v.Conflicts)
	}
}
