package server

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

// Passkeys sign in instead of the password. A passkey lives wherever the
// owner keeps them (the phone's or computer's own keychain, a password
// manager, a security key) and is opened there with Face ID, a fingerprint
// or the device's PIN; Glowworm only stores the public half. Signing in with
// one gives the same long-lived session as the password does.
const (
	ceremonyCookie = "glowworm_webauthn"
	ceremonyTTL    = 5 * time.Minute
)

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: isTLS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// safeNext is a local path to go on to.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
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

const passkeyHostMessage = "Passkeys only work when Glowworm is opened at a domain name over HTTPS"

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

// knownAuthenticators names the common passkey providers by AAGUID. Browsers
// may blank it, so it's only the first guess.
var knownAuthenticators = map[string]string{
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"dd4ec289-e01d-41c9-bb89-70fa845d4bf2": "iCloud Keychain",
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	"adce0002-35bc-c60a-648b-0b25f1f05503": "Chrome on Mac",
	"08987058-cadc-4b81-b6e1-30de50dcbe96": "Windows Hello",
	"9ddd1817-af5a-4672-a2b9-3e3dd95000a9": "Windows Hello",
	"6028b017-b1d4-4c02-b4b3-afcdafc96bb2": "Windows Hello",
	"bada5566-a7aa-401f-bd96-45619a55120d": "1Password",
	"d548826e-79b4-db40-a3d8-11116f7e8349": "Bitwarden",
	"531126d6-e717-415c-9320-3d9aa6981239": "Dashlane",
}

func formatAAGUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// passkeyName guesses where a passkey is kept, to tell them apart: a
// known provider, a phone or security key, or else the device it was added
// on.
func passkeyName(cred *webauthn.Credential, ua string) string {
	if name, ok := knownAuthenticators[formatAAGUID(cred.Authenticator.AAGUID)]; ok {
		return name
	}
	if cred.Authenticator.Attachment == protocol.CrossPlatform {
		for _, t := range cred.Transport {
			if t == protocol.Hybrid {
				return "Phone"
			}
		}
		return "Security key"
	}
	return deviceName(ua)
}

// deviceName names the kind of device a user agent is.
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

// apiPasskeyRegisterBegin starts adding a passkey. Any authenticator will
// do: this device's own, a password manager, a phone nearby or a security
// key. It has to be discoverable, so it can sign in without a username.
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
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
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

// apiPasskeyRegisterFinish stores the new passkey.
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
		return &acctValidationError{Message: "The passkey couldn't be added. Try again."}
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	key := &repos.Passkey{
		UserID: sess.UserID, CredentialID: base64.RawURLEncoding.EncodeToString(cred.ID),
		Credential: string(b), Name: passkeyName(cred, r.UserAgent()),
	}
	if err := a.Repos.Passkeys.Add(key); err != nil {
		return err
	}
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
	writeJSON(w, map[string]any{"items": items})
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

// passkeySettings is the Passkeys section of Settings.
type passkeySettings struct {
	Passkeys []passkeyView
	// HostOK is false when Glowworm was opened somewhere passkeys can't
	// work (an IP address, or plain http).
	HostOK bool
}

func (a *App) passkeySettings(r *http.Request) (*passkeySettings, error) {
	sess, ok := a.session(r)
	if !ok {
		return nil, nil
	}
	keys, err := a.passkeyList(sess.UserID)
	if err != nil {
		return nil, err
	}
	_, hostOK := passkeyHost(r)
	return &passkeySettings{Passkeys: keys, HostOK: hostOK}, nil
}

// ---- Signing in with a passkey

// handlePasskeyLoginBegin asks the browser for any passkey it has for
// Glowworm; the passkey itself says whose it is.
func (a *App) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	a.jsonHandler(w, r, func() error {
		if a.Cfg.AuthMode != "password" || !a.Audit.HasUser() {
			return &badRequest{"There's no account to sign in to"}
		}
		wa, err := a.webAuthn(r)
		if err != nil {
			return err
		}
		assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return err
		}
		if err := a.saveCeremony(w, r, "login", sd); err != nil {
			return err
		}
		writeJSON(w, assertion)
		return nil
	})
}

// handlePasskeyLoginFinish checks the passkey and signs its owner in. A
// passkey that doesn't check out counts against the same limit as a wrong
// password.
func (a *App) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	a.jsonHandler(w, r, func() error {
		if a.Cfg.AuthMode != "password" {
			return &badRequest{"Passkeys aren't used here"}
		}
		key := "login:" + limitKey(clientIP(r, a.Cfg.TrustedProxies))
		if a.loginLimiter.Blocked(key) {
			return &acctValidationError{Message: "Too many failed sign-ins from this device. Wait 15 minutes and try again."}
		}
		sd, err := a.takeCeremony(w, r, "login")
		if err != nil {
			return err
		}
		wa, err := a.webAuthn(r)
		if err != nil {
			return err
		}
		found, cred, err := wa.FinishPasskeyLogin(func(_, userHandle []byte) (webauthn.User, error) {
			return a.waUser(string(userHandle))
		}, *sd, r)
		if err != nil {
			a.loginLimiter.Fail(key)
			a.Log.Warn("passkey sign-in", "err", err)
			return &acctValidationError{Message: "That passkey didn't sign in. It may have been removed from Glowworm; sign in with your password."}
		}
		a.loginLimiter.Reset(key)
		if b, err := json.Marshal(cred); err == nil {
			if err := a.Repos.Passkeys.Used(base64.RawURLEncoding.EncodeToString(cred.ID), string(b)); err != nil {
				a.Log.Warn("passkey sign-in: saving sign count", "err", err)
			}
		}
		issueSession(w, r, a.Secret, found.(*waUser).u)
		writeJSON(w, map[string]any{"ok": true})
		return nil
	})
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
