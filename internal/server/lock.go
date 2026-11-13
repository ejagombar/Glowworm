package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"glowworm/internal/core"
	"glowworm/internal/crypto"
	"glowworm/internal/store/repos"
)

// The app lock. A device that turns it on (by setting up Face ID, or
// unlocking with a passkey it already has) gets a long-lived signed
// deviceLockCookie. On such a device every request also needs a fresh
// unlockCookie, tied to the session; it's renewed by each request and runs
// out after the lock time with none, after which pages send the device to
// /unlock and the API answers 423. Face ID (any passkey) or the password
// unlocks it again. Devices without the cookie, such as a desktop that never
// turned it on, aren't locked.
const (
	deviceLockCookie = "glowworm_lock"
	unlockCookie     = "glowworm_unlock"
	ceremonyCookie   = "glowworm_webauthn"
	deviceLockTTL    = 400 * 24 * time.Hour // browsers keep cookies no longer
	ceremonyTTL      = 5 * time.Minute
	lockAfterKey     = "lock_after_minutes"
)

// lockAfterChoices are the lock times Settings offers, in minutes. 0 locks
// whenever the app is put away (app.js does that); the server then still
// allows a minute between requests, so moving between pages doesn't lock.
var lockAfterChoices = []int{0, 1, 5, 15, 60}

func (a *App) lockAfter() int {
	m := int(a.Repos.Settings.GetFloat(lockAfterKey, 5))
	for _, c := range lockAfterChoices {
		if c == m {
			return m
		}
	}
	return 5
}

func (a *App) unlockWindow() time.Duration {
	return time.Duration(max(a.lockAfter(), 1)) * time.Minute
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: isTLS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// deviceLocked reports whether this device has the lock turned on for userID.
func (a *App) deviceLocked(r *http.Request, userID string) bool {
	c, err := r.Cookie(deviceLockCookie)
	if err != nil {
		return false
	}
	payload, ok := decodeToken(a.Secret, c.Value)
	return ok && payload == "lock|"+userID
}

func (a *App) setDeviceLock(w http.ResponseWriter, r *http.Request, userID string, on bool) {
	if !on {
		setCookie(w, r, deviceLockCookie, "", -1)
		return
	}
	setCookie(w, r, deviceLockCookie, encodeToken(a.Secret, "lock|"+userID), int(deviceLockTTL.Seconds()))
}

// lockRequired reports whether this request has to be unlocked: the device
// has the lock on and the owner still has a passkey to unlock it with.
func (a *App) lockRequired(r *http.Request, sess *Session) bool {
	if !a.deviceLocked(r, sess.UserID) {
		return false
	}
	n, err := a.Repos.Passkeys.Count(sess.UserID)
	return err == nil && n > 0
}

func writeUnlock(w http.ResponseWriter, r *http.Request, secret *crypto.Secret, nonce string, until time.Time) {
	setCookie(w, r, unlockCookie, encodeToken(secret, fmt.Sprintf("unlock|%s|%d", nonce, until.Unix())), 0)
}

func (a *App) setUnlocked(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeUnlock(w, r, a.Secret, sess.Nonce, time.Now().Add(a.unlockWindow()))
}

// unlockedUntil is when this session's unlock runs out, if it hasn't.
func (a *App) unlockedUntil(r *http.Request, sess *Session) (time.Time, bool) {
	c, err := r.Cookie(unlockCookie)
	if err != nil {
		return time.Time{}, false
	}
	payload, ok := decodeToken(a.Secret, c.Value)
	if !ok {
		return time.Time{}, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 3 || parts[0] != "unlock" || parts[1] != sess.Nonce {
		return time.Time{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() >= exp {
		return time.Time{}, false
	}
	return time.Unix(exp, 0), true
}

// lockExempt are the paths a locked device can still reach: the unlock page
// and what it posts to, and signing out.
func lockExempt(path string) bool {
	return path == "/unlock" || strings.HasPrefix(path, "/unlock/") || path == "/logout"
}

// checkLock is withAuth's lock step: it answers a locked request itself and
// returns false, or renews the unlock and returns true.
func (a *App) checkLock(w http.ResponseWriter, r *http.Request, sess *Session) bool {
	if lockExempt(r.URL.Path) || !a.lockRequired(r, sess) {
		return true
	}
	until, ok := a.unlockedUntil(r, sess)
	if !ok {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
			apiError(w, http.StatusLocked, "locked", "Glowworm is locked. Unlock it to carry on.")
			return false
		}
		http.Redirect(w, r, "/unlock?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return false
	}
	// The data version check runs every minute by itself; only what the
	// person does keeps the app unlocked. Renew at most every 30s.
	if r.URL.Path != "/api/v1/version" && time.Until(until) < a.unlockWindow()-30*time.Second {
		a.setUnlocked(w, r, sess)
	}
	return true
}

// safeNext is a local path to go to after unlocking.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/unlock") {
		return "/"
	}
	return next
}

// ---- WebAuthn

type waUser struct {
	u     *core.User
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte   { return []byte(u.u.ID) }
func (u *waUser) WebAuthnName() string { return u.u.Username }
func (u *waUser) WebAuthnDisplayName() string {
	if u.u.DisplayName != "" {
		return u.u.DisplayName
	}
	return u.u.Username
}
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (a *App) waUser(userID string) (*waUser, error) {
	u, err := a.Repos.Users.Get(userID)
	if err != nil {
		return nil, err
	}
	keys, err := a.Repos.Passkeys.ForUser(userID)
	if err != nil {
		return nil, err
	}
	wu := &waUser{u: u}
	for _, k := range keys {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Credential), &c); err != nil {
			return nil, err
		}
		wu.creds = append(wu.creds, c)
	}
	return wu, nil
}

const passkeyHostMessage = "Face ID only works when Glowworm is opened at a domain name over HTTPS"

// passkeyHost is the domain passkeys belong to: the one Glowworm was opened
// at. Browsers refuse passkeys for an IP address or a plain-http page.
func passkeyHost(r *http.Request) (string, bool) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return host, true
	}
	return host, isTLS(r) && net.ParseIP(host) == nil && host != ""
}

func (a *App) webAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	host, ok := passkeyHost(r)
	if !ok {
		return nil, &acctValidationError{Message: passkeyHostMessage}
	}
	scheme := "http"
	if isTLS(r) {
		scheme = "https"
	}
	origin := scheme + "://" + r.Host
	// Behind a proxy on another port the Host header loses the port; the
	// browser's Origin still has it.
	if o, err := url.Parse(r.Header.Get("Origin")); err == nil && o.Hostname() == host {
		origin = o.Scheme + "://" + o.Host
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL}
	return webauthn.New(&webauthn.Config{
		RPID: host, RPDisplayName: "Glowworm", RPOrigins: []string{origin},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

// A ceremony's challenge waits in a signed cookie between begin and finish.
type ceremony struct {
	Kind    string               `json:"kind"`
	Session webauthn.SessionData `json:"session"`
}

func (a *App) saveCeremony(w http.ResponseWriter, r *http.Request, kind string, sd *webauthn.SessionData) error {
	b, err := json.Marshal(ceremony{Kind: kind, Session: *sd})
	if err != nil {
		return err
	}
	setCookie(w, r, ceremonyCookie, encodeToken(a.Secret, string(b)), int(ceremonyTTL.Seconds()))
	return nil
}

func (a *App) takeCeremony(w http.ResponseWriter, r *http.Request, kind string) (*webauthn.SessionData, error) {
	setCookie(w, r, ceremonyCookie, "", -1)
	c, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return nil, &badRequest{"That took too long. Try again."}
	}
	payload, ok := decodeToken(a.Secret, c.Value)
	var cer ceremony
	if !ok || json.Unmarshal([]byte(payload), &cer) != nil || cer.Kind != kind {
		return nil, &badRequest{"That took too long. Try again."}
	}
	return &cer.Session, nil
}

// deviceName guesses what a passkey was set up on, to tell them apart.
func deviceName(ua string) string {
	for _, d := range []struct{ match, name string }{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"},
		{"Macintosh", "Mac"}, {"Windows", "Windows PC"}, {"CrOS", "Chromebook"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, d.match) {
			return d.name
		}
	}
	return "Passkey"
}

func (a *App) currentSession(r *http.Request) (*Session, error) {
	sess, ok := a.session(r)
	if !ok {
		return nil, &acctValidationError{Message: "Not signed in"}
	}
	return sess, nil
}

// apiPasskeyRegisterBegin starts setting up Face ID on this device.
func (a *App) apiPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.currentSession(r)
	if err != nil {
		return err
	}
	wa, err := a.webAuthn(r)
	if err != nil {
		return err
	}
	user, err := a.waUser(sess.UserID)
	if err != nil {
		return err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for i := range user.creds {
		exclude = append(exclude, user.creds[i].Descriptor())
	}
	creation, sd, err := wa.BeginRegistration(user,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			AuthenticatorAttachment: protocol.Platform,
			ResidentKey:             protocol.ResidentKeyRequirementPreferred,
			UserVerification:        protocol.VerificationRequired,
		}))
	if err != nil {
		return err
	}
	if err := a.saveCeremony(w, r, "register", sd); err != nil {
		return err
	}
	writeJSON(w, creation)
	return nil
}

// apiPasskeyRegisterFinish stores the new passkey and turns the lock on here.
func (a *App) apiPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.currentSession(r)
	if err != nil {
		return err
	}
	sd, err := a.takeCeremony(w, r, "register")
	if err != nil {
		return err
	}
	wa, err := a.webAuthn(r)
	if err != nil {
		return err
	}
	user, err := a.waUser(sess.UserID)
	if err != nil {
		return err
	}
	cred, err := wa.FinishRegistration(user, *sd, r)
	if err != nil {
		a.Log.Warn("passkey registration", "err", err)
		return &acctValidationError{Message: "Face ID couldn't be set up. Try again."}
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	key := &repos.Passkey{
		UserID: sess.UserID, CredentialID: base64.RawURLEncoding.EncodeToString(cred.ID),
		Credential: string(b), Name: deviceName(r.UserAgent()),
	}
	if err := a.Repos.Passkeys.Add(key); err != nil {
		return err
	}
	a.setDeviceLock(w, r, sess.UserID, true)
	a.setUnlocked(w, r, sess)
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiPasskeys(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.currentSession(r)
	if err != nil {
		return err
	}
	items, err := a.passkeyList(sess.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": items, "device_locked": a.deviceLocked(r, sess.UserID)})
	return nil
}

type passkeyView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Created  string `json:"created"`
	LastUsed string `json:"last_used"`
}

func (a *App) passkeyList(userID string) ([]passkeyView, error) {
	keys, err := a.Repos.Passkeys.ForUser(userID)
	if err != nil {
		return nil, err
	}
	out := []passkeyView{}
	for _, k := range keys {
		out = append(out, passkeyView{ID: k.ID, Name: k.Name, Created: shortTimestamp(k.CreatedAt), LastUsed: shortTimestamp(k.LastUsedAt)})
	}
	return out, nil
}

func shortTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}

func (a *App) apiPasskeyDelete(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.currentSession(r)
	if err != nil {
		return err
	}
	ok, err := a.Repos.Passkeys.Delete(sess.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !ok {
		return repos.ErrNotFound
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// apiLockDevice turns the lock off on this device. Turning it on goes
// through setting up Face ID or unlocking with a passkey.
func (a *App) apiLockDevice(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.currentSession(r)
	if err != nil {
		return err
	}
	var in struct {
		On bool `json:"on"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if in.On {
		return &acctValidationError{Message: "Set up Face ID to turn the lock on"}
	}
	a.setDeviceLock(w, r, sess.UserID, false)
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiLockAfter(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Minutes int `json:"minutes"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	valid := false
	for _, c := range lockAfterChoices {
		valid = valid || c == in.Minutes
	}
	if !valid {
		return &acctValidationError{Message: "Choose one of the lock times offered"}
	}
	if err := a.Repos.Settings.Set(nil, lockAfterKey, in.Minutes); err != nil {
		return err
	}
	if sess, ok := a.session(r); ok {
		a.setUnlocked(w, r, sess)
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// lockSettings is the Face ID section of Settings.
type lockSettings struct {
	Passkeys     []passkeyView
	DeviceLocked bool
	After        int
	Choices      []int
	// HostOK is false when Glowworm was opened somewhere passkeys can't
	// work (an IP address, or plain http).
	HostOK bool
}

func (a *App) lockSettings(r *http.Request) (*lockSettings, error) {
	sess, ok := a.session(r)
	if !ok {
		return nil, nil
	}
	keys, err := a.passkeyList(sess.UserID)
	if err != nil {
		return nil, err
	}
	_, hostOK := passkeyHost(r)
	return &lockSettings{
		Passkeys: keys, DeviceLocked: a.deviceLocked(r, sess.UserID) && len(keys) > 0,
		After: a.lockAfter(), Choices: lockAfterChoices, HostOK: hostOK,
	}, nil
}

// ---- The unlock page

func (a *App) handleUnlockPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	sess, ok := a.session(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !a.lockRequired(r, sess) {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	// app.js locks with ?lock=1 when the app comes back after the lock
	// time, which the server can't see by itself (the page was only hidden).
	if r.URL.Query().Get("lock") == "1" {
		setCookie(w, r, unlockCookie, "", -1)
	} else if _, ok := a.unlockedUntil(r, sess); ok {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	errMsg := ""
	switch r.URL.Query().Get("e") {
	case "1":
		errMsg = "Incorrect password."
	case "2":
		errMsg = "Too many failed attempts from this device. Wait 15 minutes and try again."
	}
	base, err := a.base(w, r, "unlock", "Locked")
	if err != nil {
		a.Log.Error("unlock page", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	base.Standalone = true
	base.LockAfter = -1 // this page is the lock
	data := struct {
		*viewBase
		Next, Error string
	}{viewBase: base, Next: next, Error: errMsg}
	if err := a.Render.Render(w, "unlock", data); err != nil {
		a.Log.Error("unlock page", "err", err)
	}
}

// handleUnlockBegin asks for any of the owner's passkeys. The settings page
// uses it too, to turn the lock on with a passkey the device already has.
func (a *App) handleUnlockBegin(w http.ResponseWriter, r *http.Request) {
	a.jsonHandler(w, r, func() error {
		sess, err := a.currentSession(r)
		if err != nil {
			return err
		}
		wa, err := a.webAuthn(r)
		if err != nil {
			return err
		}
		user, err := a.waUser(sess.UserID)
		if err != nil {
			return err
		}
		if len(user.creds) == 0 {
			return &acctValidationError{Message: "Face ID isn't set up yet"}
		}
		assertion, sd, err := wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return err
		}
		if err := a.saveCeremony(w, r, "unlock", sd); err != nil {
			return err
		}
		writeJSON(w, assertion)
		return nil
	})
}

func (a *App) handleUnlockFinish(w http.ResponseWriter, r *http.Request) {
	a.jsonHandler(w, r, func() error {
		sess, err := a.currentSession(r)
		if err != nil {
			return err
		}
		sd, err := a.takeCeremony(w, r, "unlock")
		if err != nil {
			return err
		}
		wa, err := a.webAuthn(r)
		if err != nil {
			return err
		}
		user, err := a.waUser(sess.UserID)
		if err != nil {
			return err
		}
		cred, err := wa.FinishLogin(user, *sd, r)
		if err != nil {
			a.Log.Warn("passkey unlock", "err", err)
			return &acctValidationError{Message: "That didn't unlock Glowworm. Try again."}
		}
		if b, err := json.Marshal(cred); err == nil {
			if err := a.Repos.Passkeys.Used(base64.RawURLEncoding.EncodeToString(cred.ID), string(b)); err != nil {
				a.Log.Warn("passkey unlock: saving sign count", "err", err)
			}
		}
		a.setDeviceLock(w, r, sess.UserID, true)
		a.setUnlocked(w, r, sess)
		writeJSON(w, map[string]any{"ok": true})
		return nil
	})
}

// handleUnlockPassword is the fallback when Face ID isn't to hand.
func (a *App) handleUnlockPassword(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.PostFormValue("next"))
	back := "/unlock?next=" + url.QueryEscape(next)
	sess, ok := a.session(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	key := "login:" + limitKey(clientIP(r, a.Cfg.TrustedProxies))
	if a.loginLimiter.Blocked(key) {
		http.Redirect(w, r, back+"&e=2", http.StatusFound)
		return
	}
	u, err := a.Repos.Users.Get(sess.UserID)
	if err != nil || !verifyPassword(r.PostFormValue("password"), u.PasswordHash) {
		a.loginLimiter.Fail(key)
		http.Redirect(w, r, back+"&e=1", http.StatusFound)
		return
	}
	a.loginLimiter.Reset(key)
	a.setUnlocked(w, r, sess)
	http.Redirect(w, r, next, http.StatusFound)
}

// jsonHandler runs fn and answers its error as the API does.
func (a *App) jsonHandler(w http.ResponseWriter, r *http.Request, fn func() error) {
	if err := fn(); err != nil {
		if serviceError(w, err) {
			return
		}
		a.Log.Error("api", "path", r.URL.Path, "err", err)
		apiError(w, http.StatusInternalServerError, "internal", "Something went wrong on the server (the log has the details).")
	}
}
