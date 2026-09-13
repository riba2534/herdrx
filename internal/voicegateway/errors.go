package voicegateway

import "errors"

// Failure is a normalized, user-facing failure. Message is always actionable
// Simplified Chinese and never contains an upstream address, credential or
// account identifier.
type Failure struct {
	Code       string
	Message    string
	RetryAfter int // seconds; 0 means "no retry hint"
	status     int
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

// Status is the HTTP status the route should use for this failure.
func (f *Failure) Status() int {
	if f.status != 0 {
		return f.status
	}
	return 400
}

func failure(status int, code, message string) *Failure {
	return &Failure{Code: code, Message: message, status: status}
}

// AsFailure extracts a *Failure from an error chain, if present.
func AsFailure(err error) (*Failure, bool) {
	var target *Failure
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// Pre-built failures. Routes map these directly; nothing here is constructed
// from upstream text.
var (
	// ErrNotConfigured is returned by Gateway operations while disabled.
	ErrNotConfigured = failure(404, "voice_disabled", "本实例未启用语音输入")

	// ErrAudioDisabled rejects an audio-reply request when the operator left
	// audio off.
	ErrAudioDisabled = failure(403, "voice_audio_disabled", "本实例未开启语音回答，请改用文字回复模式")

	// ErrBusy is returned when a concurrency cap is reached. It never queues.
	ErrBusy = failure(429, "voice_busy", "语音会话数已达上限，请先结束一个语音会话再试")

	// ErrRateLimited is returned when one user creates sessions too quickly.
	ErrRateLimited = failure(429, "voice_rate_limited", "语音会话创建过于频繁，请稍后再试")

	// ErrTicket is returned for a missing, expired, reused or mismatched
	// ticket. It never says which, so a caller cannot probe ticket state.
	ErrTicket = failure(403, "voice_ticket_invalid", "语音会话票据无效或已过期，请重新点击麦克风")

	// ErrUpstream is returned when the upstream session cannot be established.
	ErrUpstream = failure(502, "voice_upstream_unavailable", "语音上游暂时不可用，请稍后重试")

	// ErrPayload is returned for a malformed or over-limit browser frame.
	ErrPayload = failure(400, "voice_invalid_frame", "语音数据帧不合法或超出上限")
)
