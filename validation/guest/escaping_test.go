package guest_test

import (
	"bytes"
	"strings"
	"testing"
)

func TestSyntheticEscapedBodyBoundary(t *testing.T) {
	b := newBroker()
	b.failDocs = false
	for _, n := range []int{bodyLimit, bodyLimit + 1} {
		state := object{"source": strings.Repeat("\"\\<>&\t界", 1000), "padding": ""}
		req := object{"model": model, "state": state, "questions": object{"DC-01": object{"type": "Noul"}}}
		state["padding"] = strings.Repeat("x", n-len(raw(req)))
		body := raw(req)
		if len(body) != n {
			t.Fatal("bad boundary fixture")
		}
		params := raw(object{"operation": "http.post", "target": object{"url": endpoint}, "arguments": object{"content_type": "application/json", "body": string(body)}})
		result, er := b.dispatch("invoke.call", params)
		if er != nil || (n == bodyLimit && objectOf(t, result)["status"] != "succeeded") || (n > bodyLimit && !bytes.Contains(result, []byte("NET_REQUEST_TOO_BIG"))) {
			t.Fatalf("encoded boundary n=%d result=%s error=%s", n, result, er)
		}
		t.Logf("decoded serialized HTTP body=%d encoded BBB params=%d accepted=%t", n, len(params), n == bodyLimit)
	}
}
