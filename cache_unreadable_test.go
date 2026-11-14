package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func TestAFailedCacheReadIsNamedAsOne(t *testing.T) {
	for _, inner := range []error{sdk.Fail("FS_IO", "the host could not read the file"), errors.New("no reply from the host")} {
		_, err := failure(fmt.Errorf("%w: %w", cq.ErrCache, inner))
		if reasonOf(err) != "CACHE_UNREADABLE" || !strings.Contains(wordsOf(err), "the judge was not asked for what that lookup covers") || !strings.Contains(wordsOf(err), inner.Error()) {
			t.Fatalf("a failed cache read under %v is named %q: %v", inner, reasonOf(err), err)
		}

		for _, claim := range []string{"asked nothing", "nothing was recorded for this file"} {
			if strings.Contains(wordsOf(err), claim) {
				t.Fatalf("the words claim %q, which is false when an earlier battery of the file was asked and saved: %s", claim, wordsOf(err))
			}
		}
		if !strings.Contains(wordsOf(err), "Anything already answered and recorded for the file stands") {
			t.Fatalf("the words do not say that what was done stands: %s", wordsOf(err))
		}
	}
	if _, err := failure(sdk.Fail("FS_IO", "the host could not read the file")); reasonOf(err) != "FS_IO" {
		t.Fatalf("a host failure outside the cache keeps its own name, and it was named %q", reasonOf(err))
	}
}

func TestOnlyNotFoundIsAnAbsentFolder(t *testing.T) {
	if !notFound(sdk.Fail("FS_NOT_FOUND", "no such folder")) || !notFound(fmt.Errorf("list: %w", sdk.Fail("FS_NOT_FOUND", "no such folder"))) {
		t.Fatal("the host's FS_NOT_FOUND is not read as not found")
	}
	for _, err := range []error{nil, sdk.Fail("FS_IO", "the host could not read the folder"), errors.New("no reply from the host")} {
		if notFound(err) {
			t.Fatalf("%v is read as an absent folder", err)
		}
	}
}
