// Package notify sends Web Push notifications to the devices that allowed
// them, and the reminders to bring out-of-date balances up to date.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"glowworm/internal/crypto"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// Settings keys.
const (
	keyVAPIDPublic  = "push_vapid_public"
	keyVAPIDPrivate = "push_vapid_private" // encrypted with the instance secret
)

type Service struct {
	DB     *store.DB
	Secret *crypto.Secret
	// Client sends to the push services; http.DefaultClient with a timeout
	// when nil.
	Client webpush.HTTPClient

	mu sync.Mutex
}

// Subscription is a device that allowed notifications.
type Subscription struct {
	ID         string `json:"id"`
	Endpoint   string `json:"-"`
	P256dh     string `json:"-"`
	Auth       string `json:"-"`
	Label      string `json:"label"`
	Origin     string `json:"-"`
	CreatedAt  string `json:"created_at"`
	LastSentAt string `json:"last_sent_at"`
	LastError  string `json:"last_error"`
	// Service is the push service's host, to tell devices apart.
	Service string `json:"service"`
}

// Message is what a notification shows; URL is opened when it's tapped.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
	// Badge is the number the app's icon shows (0 leaves it alone).
	Badge int `json:"badge,omitempty"`
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// PublicKey is the VAPID public key browsers subscribe with, made the first
// time it's asked for.
func (s *Service) PublicKey() (string, error) {
	pub, _, err := s.keys()
	return pub, err
}

func (s *Service) keys() (pub, priv string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cipher string
	err = s.DB.QueryRow(`SELECT (SELECT value FROM settings WHERE key = ?), (SELECT value FROM settings WHERE key = ?)`,
		keyVAPIDPublic, keyVAPIDPrivate).Scan(nullStr(&pub), nullStr(&cipher))
	if err != nil {
		return "", "", err
	}
	pub, cipher = unquote(pub), unquote(cipher)
	if pub != "" && cipher != "" {
		b, err := s.Secret.Decrypt(cipher)
		if err != nil {
			return "", "", fmt.Errorf("push keys: %w", err)
		}
		return pub, string(b), nil
	}
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	enc, err := s.Secret.Encrypt([]byte(priv))
	if err != nil {
		return "", "", err
	}
	now := util.NowStr()
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		for k, v := range map[string]string{keyVAPIDPublic: pub, keyVAPIDPrivate: enc} {
			b, _ := json.Marshal(v)
			if _, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, string(b), now); err != nil {
				return err
			}
		}
		return nil
	})
	return pub, priv, err
}

// Subscribe records a device's subscription, or refreshes it.
func (s *Service) Subscribe(endpoint, p256dh, auth, label, origin string) (*Subscription, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(endpoint) > 2000 {
		return nil, &ValidationError{"That isn't a push subscription this server can use"}
	}
	if p256dh == "" || auth == "" || len(p256dh) > 200 || len(auth) > 100 {
		return nil, &ValidationError{"The subscription's keys are missing"}
	}
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		label = label[:80]
	}
	id := util.NewID()
	now := util.NowStr()
	_, err = s.DB.Exec(`INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, label, origin, created_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth,
			label = excluded.label, origin = excluded.origin, last_error = ''`,
		id, endpoint, p256dh, auth, label, origin, now)
	if err != nil {
		return nil, err
	}
	return s.byEndpoint(endpoint)
}

// Unsubscribe forgets a device by its endpoint.
func (s *Service) Unsubscribe(endpoint string) error {
	_, err := s.DB.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// Remove forgets a device by id.
func (s *Service) Remove(id string) error {
	_, err := s.DB.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, id)
	return err
}

const subCols = `id, endpoint, p256dh, auth, label, origin, created_at, COALESCE(last_sent_at, ''), last_error`

func scanSub(sc interface{ Scan(...any) error }) (*Subscription, error) {
	var sub Subscription
	if err := sc.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Label, &sub.Origin, &sub.CreatedAt,
		&sub.LastSentAt, &sub.LastError); err != nil {
		return nil, err
	}
	if u, err := url.Parse(sub.Endpoint); err == nil {
		sub.Service = u.Host
	}
	return &sub, nil
}

func (s *Service) byEndpoint(endpoint string) (*Subscription, error) {
	return scanSub(s.DB.QueryRow(`SELECT `+subCols+` FROM push_subscriptions WHERE endpoint = ?`, endpoint))
}

// List is every subscribed device, oldest first.
func (s *Service) List() ([]*Subscription, error) {
	rows, err := s.DB.Query(`SELECT ` + subCols + ` FROM push_subscriptions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// Result is how sending to one device went.
type Result struct {
	Subscription *Subscription
	Err          error
	// Gone says the push service no longer knows the device (it was
	// unsubscribed or reset), so it has been forgotten.
	Gone bool
}

// ErrNoDevices is sending with no device subscribed.
var ErrNoDevices = errors.New("no devices have notifications turned on")

// SendAll sends m to every subscribed device.
func (s *Service) SendAll(ctx context.Context, m Message) ([]Result, error) {
	subs, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, ErrNoDevices
	}
	var out []Result
	for _, sub := range subs {
		out = append(out, s.Send(ctx, sub, m))
	}
	return out, nil
}

// SendTo sends m to the device with endpoint.
func (s *Service) SendTo(ctx context.Context, endpoint string, m Message) (Result, error) {
	sub, err := s.byEndpoint(endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, &ValidationError{"This device isn't subscribed. Turn notifications on first."}
	}
	if err != nil {
		return Result{}, err
	}
	return s.Send(ctx, sub, m), nil
}

// Send sends m to one device, records how it went, and forgets a device
// its push service says is gone.
func (s *Service) Send(ctx context.Context, sub *Subscription, m Message) Result {
	res := Result{Subscription: sub}
	pub, priv, err := s.keys()
	if err != nil {
		res.Err = err
		return res
	}
	payload, _ := json.Marshal(m)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient: client, Subscriber: subject(sub.Origin), TTL: 24 * 60 * 60,
		Urgency: webpush.UrgencyNormal, Topic: topic(m.Tag),
		VAPIDPublicKey: pub, VAPIDPrivateKey: priv,
	})
	if err == nil {
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			res.Gone = true
			err = fmt.Errorf("the push service no longer knows this device")
		case resp.StatusCode >= 300:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			err = fmt.Errorf("the push service answered %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	res.Err = err
	now := util.NowStr()
	switch {
	case res.Gone:
		_ = s.Remove(sub.ID)
	case err != nil:
		_, _ = s.DB.Exec(`UPDATE push_subscriptions SET last_error = ? WHERE id = ?`, truncate(err.Error(), 300), sub.ID)
	default:
		_, _ = s.DB.Exec(`UPDATE push_subscriptions SET last_sent_at = ?, last_error = '' WHERE id = ?`, now, sub.ID)
	}
	return res
}

// subject is the contact in the VAPID token: the site's own address when it
// is served over https, which is how it's normally reached.
func subject(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return origin
	}
	return "glowworm@localhost"
}

// topic lets a newer notification replace an undelivered older one with the
// same tag. The header allows up to 32 URL-safe base64 characters.
func topic(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() == 32 {
			break
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func unquote(s string) string {
	var out string
	if json.Unmarshal([]byte(s), &out) == nil {
		return out
	}
	return s
}

type nullString struct{ dst *string }

func (n nullString) Scan(v any) error {
	switch t := v.(type) {
	case nil:
		*n.dst = ""
	case string:
		*n.dst = t
	case []byte:
		*n.dst = string(t)
	default:
		*n.dst = fmt.Sprint(t)
	}
	return nil
}

func nullStr(dst *string) any { return nullString{dst} }
