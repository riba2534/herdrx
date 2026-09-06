package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/riba2534/herdrx/internal/store"
)

type contextKey int

const (
	userContextKey contextKey = iota
	sessionContextKey
	accessContextKey
)

func userFromContext(ctx context.Context) store.User {
	user, _ := ctx.Value(userContextKey).(store.User)
	return user
}

func accessFromContext(ctx context.Context) *accessLease {
	lease, _ := ctx.Value(accessContextKey).(*accessLease)
	return lease
}

func sessionFromContext(ctx context.Context) store.Session {
	session, _ := ctx.Value(sessionContextKey).(store.Session)
	return session
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(request.RemoteAddr)
}
