package server

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"glowworm/internal/crypto"
)

const (
	sessionCookie = "glowworm_session"
	csrfCookie    = "glowworm_csrf"
	privacyCookie = "glowworm_privacy"
	sessionTTL    = 30 * 24 * time.Hour
)

type Session struct {
	UserID  string
	Expires time.Time
}

func encodeToken(secret *crypto.Secret, payload string) string {
	sig := secret.Sign([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

func decodeToken(secret *crypto.Secret, token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	if !secret.VerifySign(raw, parts[1]) {
		return "", false
	}
	return string(raw), true
}

func issueSession(w http.ResponseWriter, r *http.Request, secret *crypto.Secret, userID string) {
	secure := isTLS(r)
	payload := fmt.Sprintf("%s|%d|%s", userID, time.Now().Add(sessionTTL).Unix(), randomHex(8))
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: encodeToken(secret, payload), Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: randomHex(16), Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	secure := isTLS(r)
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookie,
			Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
	}
}

func readSession(r *http.Request, secret *crypto.Secret) (*Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	payload, ok := decodeToken(secret, c.Value)
	if !ok {
		return nil, false
	}
	parts := strings.SplitN(payload, "|", 3)
	if len(parts) != 3 {
		return nil, false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[1], "%d", &exp); err != nil {
		return nil, false
	}
	if time.Now().Unix() > exp {
		return nil, false
	}
	return &Session{UserID: parts[0], Expires: time.Unix(exp, 0)}, true
}

func CSRFToken(w http.ResponseWriter, r *http.Request, secret *crypto.Secret) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	tok := randomHex(16)
	secure := isTLS(r)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: tok, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	return tok
}

func checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get("X-CSRF-Token")
	if h == "" {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") ||
			strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			h = r.PostFormValue("csrf_token")
		}
	}
	return h != "" && h == c.Value
}

func isTLS(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hexEncode(b)
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}
