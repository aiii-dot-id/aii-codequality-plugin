package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	backendJev   = "jev"
	backendLocal = "local"

	jevHost      = "net.outbound:api.typesafe.ai:443"
	netLocal     = "net.local"
	jevBase      = "https://api.typesafe.ai"
	jevAddress   = "api.typesafe.ai"
	systemOne    = "/v1/systemone"
	modelsPath   = "/v1/models"
	jevDefault   = "jev-latest"
	scheduleSecs = 600
)

const (
	defaultStepMS       = 24000
	defaultMaxCallMS    = 8000
	defaultMaxCalls     = 12
	defaultRetryCalls   = 3
	defaultMaxFileBytes = 128 << 10
	maximumFileBytes    = 192 << 10
	defaultKeepScans    = 8
	defaultCacheBytes   = 32 << 20
	defaultRetryBudget  = int64(cq.DefaultRequestRetries)
)

type bound struct {
	key      string
	def, min int64
	max      int64
}

var bounds = []bound{
	{"step_ms", defaultStepMS, 1000, 85000},
	{"max_call_ms", defaultMaxCallMS, 500, 85000},
	{"max_calls_per_step", defaultMaxCalls, 1, 64},
	{"retry_calls", defaultRetryCalls, 1, 64},
	{"chunk_bytes", cq.ChunkBytes, 2048, 131072},
	{"max_file_bytes", defaultMaxFileBytes, 1024, maximumFileBytes},
	{"keep_scans", defaultKeepScans, 1, 256},
	{"cache_max_bytes", defaultCacheBytes, 1 << 20, 256 << 20},
	{"retry_budget", defaultRetryBudget, 0, int64(cq.MaxRequestRetries)},
}

type settingsReader interface {
	String(key string) (string, bool)
	Handle(key string) (string, bool)
	Int(key string) (int64, bool)
	Bool(key string) (bool, bool)
}

type config struct {
	backend   string
	handle    string
	model     string
	localBase string
	ints      map[string]int64
	scheduled bool
}

func (c config) stepMS() int         { return int(c.ints["step_ms"]) }
func (c config) maxCallMS() int      { return int(c.ints["max_call_ms"]) }
func (c config) maxCalls() int       { return int(c.ints["max_calls_per_step"]) }
func (c config) retryCalls() int     { return int(c.ints["retry_calls"]) }
func (c config) chunkBytes() int     { return int(c.ints["chunk_bytes"]) }
func (c config) maxFileBytes() int64 { return c.ints["max_file_bytes"] }
func (c config) keepScans() int      { return int(c.ints["keep_scans"]) }
func (c config) cacheBytes() int64   { return c.ints["cache_max_bytes"] }
func (c config) retryBudget() int64  { return c.ints["retry_budget"] }

func (c config) callMS(calls int) int { return min(c.maxCallMS(), c.stepMS()/max(calls, 1)) }

func loadConfig() (config, error) {
	vals, err := sdk.Settings.Load()
	if err != nil {
		return config{}, err
	}
	return configFrom(vals)
}

func configFrom(v settingsReader) (config, error) {
	c := config{backend: backendJev, model: jevDefault, ints: map[string]int64{}, scheduled: true}
	if b, ok := v.String("backend"); ok && b != "" {
		if b != backendJev && b != backendLocal {
			return c, sdk.Fail("JUDGE_CONFIGURATION_INVALID", fmt.Sprintf(configurationInvalid, "backend", "is neither jev nor local"))
		}
		c.backend = b
	}
	c.handle, _ = v.Handle("api_key")
	if m, ok := v.String("model"); ok && m != "" {
		c.model = m
	}
	if raw, ok := v.String("local_judge_url"); ok && strings.TrimSpace(raw) != "" {
		base, why := localBase(raw)
		if why != "" {
			return c, sdk.Fail("JUDGE_CONFIGURATION_INVALID", fmt.Sprintf(configurationInvalid, "local_judge_url", why))
		}
		c.localBase = base
	}
	for _, b := range bounds {
		c.ints[b.key] = b.def
		if n, ok := v.Int(b.key); ok {
			if n < b.min || n > b.max {
				return c, sdk.Fail("JUDGE_CONFIGURATION_INVALID", fmt.Sprintf(configurationInvalid, b.key, fmt.Sprintf("is %d, outside %d to %d", n, b.min, b.max)))
			}
			c.ints[b.key] = n
		}
	}
	if on, ok := v.Bool("scheduled_steps"); ok {
		c.scheduled = on
	}
	return c, nil
}

func localBase(raw string) (base, why string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return "", "is not an address"
	case u.Scheme != "http" && u.Scheme != "https":
		return "", "must begin http:// or https://"
	case u.Host == "" || u.Hostname() == "":
		return "", "names no host"
	case u.User != nil:
		return "", "must not carry a user or password"
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", "must not carry a query or fragment"
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, systemOne)
	return u.Scheme + "://" + u.Host + p, ""
}

type judgeAt struct {
	systemOne, models, auth, backend string
}

func (c config) where() (judgeAt, error) {
	switch c.backend {
	case backendLocal:
		if c.localBase == "" {
			return judgeAt{}, sdk.Fail("JUDGE_UNCONFIGURED", unconfiguredLocal)
		}
		u, _ := url.Parse(c.localBase)
		return judgeAt{c.localBase + systemOne, c.localBase + modelsPath, "", backendLocal + " " + u.Host}, nil
	default:
		if c.handle == "" {
			return judgeAt{}, sdk.Fail("JUDGE_UNCONFIGURED", unconfiguredJev)
		}
		return judgeAt{jevBase + systemOne, jevBase + modelsPath, c.handle, backendJev}, nil
	}
}

func (w judgeAt) sentTo() map[string]any {
	if w.backend == backendJev {
		return map[string]any{"backend": backendJev, "address": jevAddress}
	}
	return map[string]any{"backend": backendLocal, "address": strings.TrimPrefix(w.backend, backendLocal+" ")}
}
