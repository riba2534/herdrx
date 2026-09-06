package httpapi

import (
	"github.com/riba2534/herdrx/internal/hostruntime"
	"net/http"
	"strconv"
)

func writeHostConnectionError(w http.ResponseWriter, err error) {
	detail := hostruntime.DescribeError(err)
	status := http.StatusBadGateway
	if detail.Permanent {
		status = http.StatusConflict
	}
	if detail.Code == "host_capacity" {
		status = http.StatusServiceUnavailable
	}
	if detail.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(detail.RetryAfter.Seconds())+1)))
	}
	writeJSON(w, status, map[string]any{"code": detail.Code, "error": detail.Error(), "retryable": !detail.Permanent, "retry_after_ms": detail.RetryAfter.Milliseconds()})
}
