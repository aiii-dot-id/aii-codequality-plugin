package cq

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestModelLimitsAndBackoffClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   ServiceErrorCode
		reason RequestReason
		retry  bool
	}{
		{400, ServiceMaxTokensExceeded, RequestContextLimit, false},
		{400, "", RequestValidation, false},
		{400, "message mentions max_tokens_exceeded", RequestValidation, false},
		{413, ServiceMaxTokensExceeded, RequestBodyLimit, false},
		{422, "", RequestValidation, false},
		{415, "", RequestValidation, false},
		{401, "", RequestAuthentication, false},
		{403, "", RequestRefused, false},
		{404, "", RequestRefused, false},
		{429, "", RequestRateLimit, true},
		{503, "", RequestUnavailable, true},
		{529, "", RequestUnavailable, true},
		{0, "", RequestUnavailable, true},
		{600, "", RequestRefused, false},
	} {
		err := ClassifyHTTPFailure(tc.status, tc.code, nil, nil)
		if err == nil || err.Reason != tc.reason || err.Retryable() != tc.retry {
			t.Fatalf("status %d code %q: %+v", tc.status, tc.code, err)
		}
		var typed *RequestError
		if !errors.As(err, &typed) {
			t.Fatal("not a typed request error")
		}
		if errors.Is(err, ErrJudge) != tc.retry {
			t.Fatalf("wrong legacy retry compatibility: %v", err)
		}
		_, scheduleErr := (RequestRetry{}).Next(err, 1000, DefaultRequestRetries)
		if tc.retry && scheduleErr != nil {
			t.Fatal(scheduleErr)
		}
		if !tc.retry && !errors.Is(scheduleErr, ErrRetryNotAllowed) {
			t.Fatal("unchanged invalid request scheduled again")
		}
	}
	if err := ClassifyHTTPFailure(200, ServiceMaxTokensExceeded, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestA403IsReadByItsBody(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   error
	}{
		{403, "<html>Attention Required</html>", ErrRefused}, {403, "", ErrRefused},
		{401, `{"error":"bad key"}`, ErrKey}, {401, "", ErrKey}, {403, `{"error":"forbidden"}`, ErrKey},
		{400, `{"error":"state too large"}`, ErrRefused}, {404, "<html>", ErrRefused},
	} {
		err := ClassifyHTTPFailure(c.status, "", []byte(c.body), nil)
		if !errors.Is(err, c.want) || errors.Is(err, ErrJudge) || !strings.Contains(err.Error(), strconv.Itoa(c.status)) {
			t.Fatalf("%d %q: got %v, want %v", c.status, c.body, err, c.want)
		}
	}
}

func TestModelLimitsAndBackoffRestart(t *testing.T) {
	hint := int64(30)
	failure := ClassifyHTTPFailure(429, "", nil, &hint)
	r, err := (RequestRetry{}).Next(failure, 1000, DefaultRequestRetries)
	if err != nil {
		t.Fatal(err)
	}
	if r.NotBeforeMS != 31000 || r.Attempts != 1 {
		t.Fatalf("retry = %+v", r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var resumed RequestRetry
	if err := json.Unmarshal(data, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Ready(30999) || !resumed.Ready(31000) {
		t.Fatal("restart ignored deadline")
	}
	unchanged, err := resumed.Next(failure, 30999, DefaultRequestRetries)
	if !errors.Is(err, ErrRetryNotDue) || unchanged != resumed {
		t.Fatalf("premature retry changed state: %+v %v", unchanged, err)
	}
	resumed, err = resumed.Next(ClassifyHTTPFailure(503, "", nil, nil), 31000, DefaultRequestRetries)
	if err != nil || resumed.NotBeforeMS != 33000 {
		t.Fatalf("backoff not exponential: %+v %v", resumed, err)
	}
	for resumed.Attempts < DefaultRequestRetries {
		before := resumed
		resumed, err = resumed.Next(ClassifyHTTPFailure(529, "", nil, nil), resumed.NotBeforeMS, DefaultRequestRetries)
		if err != nil {
			t.Fatal(err)
		}
		delay := resumed.NotBeforeMS - before.NotBeforeMS
		if delay < 1000 || delay > RetryMaxBackoffSeconds*1000 {
			t.Fatalf("unbounded backoff %d", delay)
		}
	}
	if _, err := resumed.Next(failure, resumed.NotBeforeMS, DefaultRequestRetries); !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("retry budget not enforced: %v", err)
	}

	if _, err := (RequestRetry{}).Next(failure, 1000, 0); !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("zero budget scheduled a wait: %v", err)
	}
	long := RequestRetry{}
	for long.Attempts < MaxRequestRetries {
		before := long.NotBeforeMS
		long, err = long.Next(ClassifyHTTPFailure(503, "", nil, nil), max(before, 1000), MaxRequestRetries+5)
		if err != nil || long.NotBeforeMS-max(before, 1000) > RetryMaxBackoffSeconds*1000 {
			t.Fatalf("attempt %d: %+v %v", long.Attempts, long, err)
		}
	}
	if _, err := long.Next(failure, long.NotBeforeMS, MaxRequestRetries+5); !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("budget above the bound not held to it: %v", err)
	}
}

func TestModelLimitsAndBackoffHintBounds(t *testing.T) {
	for _, tc := range []struct {
		hint, want       int64
		present, clamped bool
	}{
		{-1, 1, false, false}, {0, 1, true, false}, {5, 5, true, false},
		{RetryMaxAfterSeconds, RetryMaxAfterSeconds, true, false},
		{math.MaxInt64, RetryMaxAfterSeconds, true, true},
	} {
		hint := tc.hint
		failure := ClassifyHTTPFailure(429, "", nil, &hint)
		if (failure.RetryAfterSeconds != nil) != tc.present || failure.RetryAfterClamped != tc.clamped {
			t.Fatalf("hint %+v => %+v", tc, failure)
		}
		hint = 900
		r, err := (RequestRetry{}).Next(failure, 1000, DefaultRequestRetries)
		if tc.clamped {
			if !errors.Is(err, ErrRetryAfterLimit) || r != (RequestRetry{}) {
				t.Fatalf("oversized hint scheduled premature retry: %+v %v", r, err)
			}
			continue
		}
		if err != nil || r.NotBeforeMS != 1000+tc.want*1000 {
			t.Fatalf("hint %d => %+v %v", tc.hint, r, err)
		}
	}
	hint := int64(60)
	if e := ClassifyHTTPFailure(400, "", nil, &hint); e.RetryAfterSeconds != nil {
		t.Fatal("terminal response retained retry hint")
	}

	for _, r := range []RequestRetry{{Attempts: MaxRequestRetries + 1}, {NotBeforeMS: -1}, {NotBeforeMS: 10}, {Attempts: 1}} {
		if r.Ready(100) {
			t.Fatalf("invalid state ready: %+v", r)
		}
		if _, err := r.Next(ClassifyHTTPFailure(429, "", nil, nil), 100, DefaultRequestRetries); !errors.Is(err, ErrRetryState) {
			t.Fatalf("invalid state accepted: %+v %v", r, err)
		}
	}
	for _, now := range []int64{-1, math.MaxInt64} {
		if _, err := (RequestRetry{}).Next(ClassifyHTTPFailure(503, "", nil, nil), now, DefaultRequestRetries); !errors.Is(err, ErrRetryState) {
			t.Fatalf("clock overflow %d: %v", now, err)
		}
	}
}
