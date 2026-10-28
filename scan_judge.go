package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type scanRequestState struct {
	Pin        ModelPin        `json:"pin"`
	Retry      cq.RequestRetry `json:"retry"`
	RequestKey string          `json:"request_key,omitempty"`
	Blocked    string          `json:"blocked,omitempty"`
}

func scanBinding(cfg config) (scanRequestState, error) {

	at := judgeAt{systemOne: jevBase + systemOne}
	if cfg.backend == backendLocal {
		var err error
		at, err = cfg.where()
		if err != nil {
			return scanRequestState{}, err
		}
	}
	concrete := ""
	if cfg.backend == backendLocal {
		concrete = cfg.model
	}
	pin, err := NewModelPin(cfg.backend, at.systemOne, cfg.model, concrete)
	return scanRequestState{Pin: pin}, err
}

func (j *hostJudge) bindScan(cfg config, s *cq.Scan, nowMS int64) error {
	var state scanRequestState
	if len(s.Runtime) == 0 {
		return pinError("scan_binding_missing")
	}
	if err := json.Unmarshal(s.Runtime, &state); err != nil {
		return err
	}
	if err := state.Pin.CheckTarget(cfg.backend, j.at.systemOne, cfg.model); err != nil {
		return err
	}
	if nowMS <= 0 {
		return sdk.Fail("HOST_CLOCK_UNAVAILABLE", hostClockMissing)
	}
	j.runtime = &state
	j.nowMS = nowMS

	j.retryBaseMS = nowMS + int64(cfg.stepMS()) + invokeSlackMS
	j.retryBudget = uint32(cfg.retryBudget())

	cursorBytes, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var cursor cq.Scan
	if err = json.Unmarshal(cursorBytes, &cursor); err != nil {
		return err
	}
	write := j.writeRuntimeCheckpoint
	if write == nil {
		write = func(b []byte) error { return checkpoints(s.ID).Save(b) }
	}
	j.persistRuntime = func() error {
		raw, err := json.Marshal(j.runtime)
		if err != nil {
			return err
		}
		cursor.Runtime = raw
		b, err := json.Marshal(&cursor)
		if err != nil {
			return err
		}

		if err = write(b); err != nil {
			return err
		}
		s.Runtime = raw
		return nil
	}

	request, err := state.Pin.RequestModel()
	if err != nil {
		return err
	}
	j.configured, j.model = request, request
	return nil
}

func (j *hostJudge) CacheEligible() bool {
	if j.runtime == nil {
		return false
	}
	_, ok := j.runtime.Pin.CacheModel()
	return ok && j.runtime.Blocked == ""
}

func (j *hostJudge) beforeScanRequest(body []byte) error {
	if j.runtime.Blocked != "" {
		return fmt.Errorf("scan request blocked: %s", j.runtime.Blocked)
	}
	sum := sha256.Sum256(body)
	key := hex.EncodeToString(sum[:])
	if !j.runtime.Retry.Ready(j.nowMS) {
		return fmt.Errorf("%w: retry not before %d", cq.ErrJudge, j.runtime.Retry.NotBeforeMS)
	}
	if j.runtime.RequestKey != key {
		j.runtime.RequestKey = key
		j.runtime.Retry = cq.RequestRetry{}
	}
	if !j.runtime.Retry.Ready(j.nowMS) {
		return fmt.Errorf("%w: retry not before %d", cq.ErrJudge, j.runtime.Retry.NotBeforeMS)
	}
	return nil
}

func (j *hostJudge) scanResponse(status int, raw []byte, retryAfter *int64) error {
	if failure := cq.ClassifyHTTPFailure(status, "", raw, retryAfter); failure != nil {
		if !failure.Retryable() {
			return failure
		}
		var cause error = failure
		next, err := j.runtime.Retry.Next(failure, j.retryBaseMS, j.retryBudget)
		switch {
		case errors.Is(err, cq.ErrRetryExhausted):
			next, cause = cq.RequestRetry{}, fmt.Errorf("%w; %w", failure, err)
		case err != nil:
			j.runtime.Blocked = err.Error()
		}
		j.runtime.Retry = next
		if err := j.persistRuntime(); err != nil {
			return fmt.Errorf("%w: %w", cq.ErrProgress, err)
		}
		return cause
	}
	pin, err := j.runtime.Pin.WithResponse(raw)
	if err != nil {
		j.runtime.Blocked = err.Error()
		if saveErr := j.persistRuntime(); saveErr != nil {
			return fmt.Errorf("%w: %w", cq.ErrProgress, saveErr)
		}
		return err
	}
	j.runtime.Pin = pin
	j.runtime.Retry = cq.RequestRetry{}

	if err := j.persistRuntime(); err != nil {
		return fmt.Errorf("%w: %w", cq.ErrProgress, err)
	}
	j.model, j.configured = pin.Model, pin.Model
	return nil
}

func (j *hostJudge) bindInvocation(cfg config, nowMS int64) error {
	binding, err := scanBinding(cfg)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if err = j.bindScan(cfg, &cq.Scan{Runtime: raw}, nowMS); err != nil {
		return err
	}
	j.persistRuntime = func() error { return nil }
	return nil
}
