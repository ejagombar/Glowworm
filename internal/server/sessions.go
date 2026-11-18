package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/crypto"
)

const (
	sessionCookie = "glowworm_session"
	csrfCookie    = "glowworm_csrf"
	privacyCookie = "glowworm_privacy"
	periodCookie  = "glowworm_period"
	// A sign-in lasts as long as browsers keep a cookie (Chrome and Safari
	// cap it at 400 days), and each day it's used starts that over, so a
	// device in use never has to sign in again.
	sessionTTL   = 400 * 24 * time.Hour
	sessionRenew = 24 * time.Hour
)

type Session struct {
	UserID  string
	Expires time.Time
	// Nonce tells this sign-in from others.
	Nonce string
	// Fingerprint ties the session to the password it was signed in with,
	// so changing the password signs out every other session.
	Fingerprint string
}

func passwordFingerprint(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:8])
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

func issueSession(w http.ResponseWriter, r *http.Request, secret *crypto.Secret, u *core.User) {
	writeSession(w, r, secret, &Session{UserID: u.ID, Nonce: randomHex(8), Fingerprint: passwordFingerprint(u.PasswordHash)})
	writeCSRF(w, r, randomHex(16))
}

func writeSession(w http.ResponseWriter, r *http.Request, secret *crypto.Secret, sess *Session) {
	payload := fmt.Sprintf("%s|%d|%s|%s", sess.UserID, time.Now().Add(sessionTTL).Unix(), sess.Nonce, sess.Fingerprint)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: encodeToken(secret, payload), Path: "/",
		HttpOnly: true, Secure: isTLS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func writeCSRF(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/",
		HttpOnly: false, Secure: isTLS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

// renewSession starts a session's time over, at most once a day, along with
// its CSRF cookie (keeping its value, so open pages carry on).
func renewSession(w http.ResponseWriter, r *http.Request, secret *crypto.Secret, sess *Session) {
	if time.Until(sess.Expires) > sessionTTL-sessionRenew {
		return
	}
	writeSession(w, r, secret, sess)
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		writeCSRF(w, r, c.Value)
	}
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	secure := isTLS(r)
	for _, name := range []string{sessionCookie, csrfCookie, unlockCookie, deviceLockCookie} {
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
	parts := strings.Split(payload, "|")
	if len(parts) != 4 {
		return nil, false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[1], "%d", &exp); err != nil {
		return nil, false
	}
	if time.Now().Unix() > exp {
		return nil, false
	}
	return &Session{UserID: parts[0], Expires: time.Unix(exp, 0), Nonce: parts[2], Fingerprint: parts[3]}, true
}

func CSRFToken(w http.ResponseWriter, r *http.Request, secret *crypto.Secret) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	tok := randomHex(16)
	writeCSRF(w, r, tok)
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
	return h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(c.Value)) == 1
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
