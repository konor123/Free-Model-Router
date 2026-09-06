// Package security contains the bind and bearer-token boundaries shared by the
// inference and control HTTP servers.
package security

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

var (
	ErrInvalidBind     = errors.New("invalid bind address")
	ErrNonLoopbackBind = errors.New("management bind must be loopback-only")
	ErrTokenRequired   = errors.New("bearer token is required for non-loopback inference")
)

// IsLoopbackBind reports whether bind names a loopback-only TCP listener.
// Empty hosts and wildcard addresses are deliberately not treated as local.
func IsLoopbackBind(bind string) bool {
	host, err := bindHost(bind)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateLoopbackBind rejects wildcard or non-loopback management listeners.
func ValidateLoopbackBind(bind string) error {
	if _, err := bindHost(bind); err != nil {
		return err
	}
	if !IsLoopbackBind(bind) {
		return fmt.Errorf("%w: %s", ErrNonLoopbackBind, bind)
	}
	return nil
}

// RequireInferenceAuth applies the Phase 14 policy: loopback inference does
// not need a token, while every non-loopback bind requires a non-empty bearer
// token. The bind address is validated before the handler is returned.
func RequireInferenceAuth(next http.Handler, bind, token string) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("next handler must not be nil")
	}
	if _, err := bindHost(bind); err != nil {
		return nil, err
	}
	if IsLoopbackBind(bind) {
		return next, nil
	}
	if strings.TrimSpace(token) == "" {
		return nil, ErrTokenRequired
	}
	return RequireBearer(next, token), nil
}

// RequireBearer protects a handler with a constant-time bearer-token check.
func RequireBearer(next http.Handler, token string) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	want := []byte(strings.TrimSpace(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validBearer(r.Header.Get("Authorization"), want) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fmr"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IsLoopbackRemote reports whether a request came from an IPv4/IPv6 loopback
// address. It fails closed for malformed or empty RemoteAddr values.
func IsLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func bindHost(bind string) (string, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return "", fmt.Errorf("%w: address must not be empty", ErrInvalidBind)
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil || port == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidBind, bind)
	}
	if host == "" {
		return "", fmt.Errorf("%w: wildcard host is not explicit", ErrInvalidBind)
	}
	return host, nil
}

func validBearer(header string, want []byte) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	got := []byte(parts[1])
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}
