package cq

import (
	"errors"
	"fmt"
	"math"
)

type RequestReason string

const (
	RequestContextLimit   RequestReason = "model_context_limit"
	RequestBodyLimit      RequestReason = "request_body_limit"
	RequestValidation     RequestReason = "request_validation"
	RequestAuthentication RequestReason = "authentication_required"
	RequestRateLimit      RequestReason = "rate_limited"
	RequestUnavailable    RequestReason = "judge_unavailable"
	RequestRefused        RequestReason = "refused"
)

type ServiceErrorCode string

const ServiceMaxTokensExceeded ServiceErrorCode = "max_tokens_exceeded"

const (
	RetryBaseSeconds       int64  = 1
	RetryMaxBackoffSeconds int64  = 300
	RetryMaxAfterSeconds   int64  = 86400
	MaxRequestRetries      uint32 = 8
)

type RequestError struct {
	Status            int           `json:"status"`
	Reason            RequestReason `json:"reason"`
	RetryAfterSeconds *int64        `json:"retry_after_seconds,omitempty"`
	RetryAfterClamped bool          `json:"retry_after_clamped,omitempty"`
}

func (e *RequestError) Error() string { return fmt.Sprintf("judge HTTP %d: %s", e.Status, e.Reason) }
func (e *RequestError) Retryable() bool {
	return e.Reason == RequestRateLimit || e.Reason == RequestUnavailable
}
func (e *RequestError) Unwrap() error {
	if e.Retryable() {
		return ErrJudge
	}
	if e.Reason == RequestAuthentication {
		return ErrKey
	}
	return ErrRefused
}

func ClassifyHTTPFailure(status int, code ServiceErrorCode, retryAfterS *int64) *RequestError {
	if status == 200 {
		return nil
	}
	e := &RequestError{Status: status, Reason: RequestRefused}
	switch {
	case status == 400 && code == ServiceMaxTokensExceeded:
		e.Reason = RequestContextLimit
	case status == 413:
		e.Reason = RequestBodyLimit
	case status == 400 || status == 422 || status == 415:
		e.Reason = RequestValidation
	case status == 401 || status == 403:
		e.Reason = RequestAuthentication
	case status == 429:
		e.Reason = RequestRateLimit
	case status == 0 || status >= 500 && status <= 599:
		e.Reason = RequestUnavailable
	}
	if e.Retryable() && retryAfterS != nil && *retryAfterS >= 0 {
		seconds := min(*retryAfterS, RetryMaxAfterSeconds)
		e.RetryAfterSeconds = &seconds
		e.RetryAfterClamped = *retryAfterS > seconds
	}
	return e
}

var (
	ErrRetryNotAllowed = errors.New("request failure is not retryable")
	ErrRetryExhausted  = errors.New("request retry budget exhausted")
	ErrRetryState      = errors.New("invalid request retry state or clock")
	ErrRetryNotDue     = errors.New("request retry is not due")
	ErrRetryAfterLimit = errors.New("service retry wait exceeds supported bound")
)

type RequestRetry struct {
	Attempts    uint32 `json:"attempts"`
	NotBeforeMS int64  `json:"not_before_ms"`
}

func (r RequestRetry) valid() bool {
	return r.Attempts <= MaxRequestRetries && r.NotBeforeMS >= 0 && (r.Attempts != 0 || r.NotBeforeMS == 0) && (r.Attempts == 0 || r.NotBeforeMS > 0)
}
func (r RequestRetry) Ready(nowMS int64) bool {
	return r.valid() && nowMS >= 0 && nowMS >= r.NotBeforeMS
}

func (r RequestRetry) Next(failure *RequestError, nowMS int64) (RequestRetry, error) {
	if !r.valid() || nowMS < 0 {
		return r, ErrRetryState
	}
	if failure == nil || !failure.Retryable() {
		return r, ErrRetryNotAllowed
	}
	if failure.RetryAfterClamped || failure.RetryAfterSeconds != nil && *failure.RetryAfterSeconds > RetryMaxAfterSeconds {
		return r, ErrRetryAfterLimit
	}
	if !r.Ready(nowMS) {
		return r, ErrRetryNotDue
	}
	if r.Attempts >= MaxRequestRetries {
		return r, ErrRetryExhausted
	}
	seconds := min(RetryBaseSeconds<<r.Attempts, RetryMaxBackoffSeconds)
	if failure.RetryAfterSeconds != nil {
		seconds = max(seconds, min(*failure.RetryAfterSeconds, RetryMaxAfterSeconds))
	}
	if nowMS > math.MaxInt64-seconds*1000 {
		return r, ErrRetryState
	}
	return RequestRetry{Attempts: r.Attempts + 1, NotBeforeMS: nowMS + seconds*1000}, nil
}
