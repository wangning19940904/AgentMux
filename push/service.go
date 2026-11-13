package push

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/wangning19940904/AgentMux/core"
)

// SubscriberAdmin is the administrator's subscriber identity. Events about
// this hub and its machines are addressed to it.
const SubscriberAdmin = "admin"

// SubscriberFor names the subscriber behind a request's principal. A user
// identity from the accounts work only needs a new case here.
func SubscriberFor(principal *core.Principal) string {
	if principal.IsTenant() {
		return "tenant:" + principal.TenantID
	}
	return SubscriberAdmin
}

// DefaultSubject identifies this application to push services when the hub
// has no public HTTPS address of its own.
const DefaultSubject = "https://github.com/wangning19940904/AgentMux"

const (
	defaultWindow       = 45 * time.Second
	defaultOfflineGrace = 2 * time.Minute
	sendTimeout         = 20 * time.Second
	queueSize           = 64
)

// Store keeps the key pair and the subscriptions (store.Store).
type Store interface {
	EnsurePushVAPIDKeys(ctx context.Context, keys core.PushVAPIDKeys) (*core.PushVAPIDKeys, error)
	UpsertPushSubscription(ctx context.Context, sub core.PushSubscription) error
	ListPushSubscriptions(ctx context.Context, subscriber string) ([]core.PushSubscription, error)
	GetPushSubscription(ctx context.Context, id string) (*core.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, id, subscriber string) (bool, error)
	RecordPushDelivery(ctx context.Context, id string, at time.Time, deliveryErr string) error
}

// SendOptions are the Web Push headers for one message.
type SendOptions struct {
	TTL     int    // seconds the push service keeps an undelivered message
	Urgency string // very-low, low, normal or high
	Topic   string // a newer message with the same topic replaces an undelivered one
}

// Sender delivers one encrypted message and returns the push service's HTTP
// status.
type Sender interface {
	Send(ctx context.Context, sub core.PushSubscription, payload []byte, opts SendOptions) (int, error)
}

// Options configures a Service.
type Options struct {
	Store Store
	// Sender defaults to Web Push with the stored VAPID keys.
	Sender Sender
	// Subject is the VAPID contact: an https URL or an e-mail address.
	Subject      string
	Log          *slog.Logger
	Window       time.Duration
	OfflineGrace time.Duration
	// DeviceName names a linked machine for machine notifications.
	DeviceName func(ctx context.Context, deviceID string) string

	clock clock
}

// Service owns the VAPID keys, the subscriptions and delivery.
type Service struct {
	store    Store
	sender   Sender
	log      *slog.Logger
	keys     core.PushVAPIDKeys
	notifier *Notifier
	presence *Presence

	queue  chan Batch
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// New loads the hub's key pair, creating it on first use, and starts the
// delivery worker.
func New(ctx context.Context, opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("push: a store is required")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Window <= 0 {
		opts.Window = defaultWindow
	}
	if opts.OfflineGrace <= 0 {
		opts.OfflineGrace = defaultOfflineGrace
	}
	if opts.clock == nil {
		opts.clock = realClock{}
	}
	keys, err := loadKeys(ctx, opts.Store)
	if err != nil {
		return nil, err
	}
	if opts.Sender == nil {
		opts.Sender = NewWebPushSender(*keys, opts.Subject, nil)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s := &Service{
		store: opts.Store, sender: opts.Sender, log: opts.Log, keys: *keys,
		queue: make(chan Batch, queueSize), ctx: runCtx, cancel: cancel,
	}
	s.notifier = newNotifier(opts.Window, opts.clock, s.enqueue)
	name := func(deviceID string) string {
		if opts.DeviceName == nil {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return opts.DeviceName(ctx, deviceID)
	}
	s.presence = newPresence(opts.OfflineGrace, opts.clock, name, s.notifier.Publish)
	s.wg.Add(1)
	go s.work()
	return s, nil
}

func loadKeys(ctx context.Context, st Store) (*core.PushVAPIDKeys, error) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("push: generate VAPID keys: %w", err)
	}
	// Keeps an existing pair; the fresh one is used only the first time.
	keys, err := st.EnsurePushVAPIDKeys(ctx, core.PushVAPIDKeys{PublicKey: public, PrivateKey: private})
	if err != nil {
		return nil, fmt.Errorf("push: load VAPID keys: %w", err)
	}
	return keys, nil
}

// PublicKey is the application server key a browser subscribes with.
func (s *Service) PublicKey() string { return s.keys.PublicKey }

// Publish hands an event to burst collapsing and delivery. It never blocks
// the caller on the network.
func (s *Service) Publish(e Event) {
	if s == nil {
		return
	}
	s.notifier.Publish(e)
}

// Notify sends one event to one subscriber's devices now, without the burst
// collapsing Publish applies: it is for events addressed to a person, such
// as a direct message. It never blocks the caller on the network.
func (s *Service) Notify(subscriber string, e Event) {
	if s == nil || subscriber == "" {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.deliver(s.ctx, Batch{Kind: e.Kind, Events: []Event{e}, At: e.At}, subscriber, "")
	}()
}

// DevicePresence records a linked machine connecting or disconnecting.
func (s *Service) DevicePresence(deviceID string, online bool) {
	if s == nil {
		return
	}
	s.presence.Changed(deviceID, online)
}

// ForgetDevice stops tracking an unpaired machine.
func (s *Service) ForgetDevice(deviceID string) {
	if s == nil {
		return
	}
	s.presence.Forget(deviceID)
}

// Close stops delivery. Held notifications are dropped.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.notifier.Close()
		s.cancel()
		s.wg.Wait()
	})
}

func (s *Service) enqueue(batch Batch) {
	select {
	case s.queue <- batch:
	default:
		s.log.Warn("push notification dropped: delivery queue is full", "kind", batch.Kind)
	}
}

func (s *Service) work() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case batch := <-s.queue:
			s.deliver(s.ctx, batch, SubscriberAdmin, "")
		}
	}
}

// SubscribeRequest is what a browser sends after PushManager.subscribe.
type SubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	// Kinds to receive; omitted means all of them.
	Kinds *[]string `json:"kinds,omitempty"`
	Label string    `json:"label,omitempty"`
	Lang  string    `json:"lang,omitempty"`
}

// SubscriptionID is derived from the endpoint, so the browser that holds a
// subscription can name it without the server handing out the endpoint.
func SubscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:16])
}

// ErrInvalidSubscription is a subscription a push service could not use.
var ErrInvalidSubscription = errors.New("invalid push subscription")

// Subscribe saves a browser's subscription for subscriber.
func (s *Service) Subscribe(ctx context.Context, subscriber string, req SubscribeRequest) (core.PushSubscription, error) {
	endpoint := strings.TrimSpace(req.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || len(endpoint) > 2048 {
		return core.PushSubscription{}, fmt.Errorf("%w: the endpoint must be an https URL", ErrInvalidSubscription)
	}
	if key, err := decodeKey(req.Keys.P256dh); err != nil || len(key) != 65 || key[0] != 4 {
		return core.PushSubscription{}, fmt.Errorf("%w: keys.p256dh must be an uncompressed P-256 public key", ErrInvalidSubscription)
	}
	if secret, err := decodeKey(req.Keys.Auth); err != nil || len(secret) != 16 {
		return core.PushSubscription{}, fmt.Errorf("%w: keys.auth must be 16 bytes", ErrInvalidSubscription)
	}
	kinds := core.PushKinds()
	if req.Kinds != nil {
		kinds = []string{}
		for _, kind := range *req.Kinds {
			if slices.Contains(core.PushKinds(), kind) && !slices.Contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
		}
	}
	lang := "en"
	if req.Lang == "zh" {
		lang = "zh"
	}
	label := strings.TrimSpace(req.Label)
	if len([]rune(label)) > 80 {
		label = string([]rune(label)[:80])
	}
	sub := core.PushSubscription{
		ID: SubscriptionID(endpoint), Subscriber: subscriber, Endpoint: endpoint,
		P256dh: req.Keys.P256dh, Auth: req.Keys.Auth, Kinds: kinds, Label: label, Lang: lang,
	}
	if existing, err := s.store.GetPushSubscription(ctx, sub.ID); err != nil {
		return core.PushSubscription{}, err
	} else if existing != nil {
		sub.CreatedAt = existing.CreatedAt
	}
	sub.UpdatedAt = time.Now().UTC()
	if err := s.store.UpsertPushSubscription(ctx, sub); err != nil {
		return core.PushSubscription{}, err
	}
	saved, err := s.store.GetPushSubscription(ctx, sub.ID)
	if err != nil || saved == nil {
		return sub, err
	}
	return *saved, nil
}

// Unsubscribe removes one of subscriber's subscriptions.
func (s *Service) Unsubscribe(ctx context.Context, subscriber, id string) (bool, error) {
	return s.store.DeletePushSubscription(ctx, id, subscriber)
}

// Subscriptions lists subscriber's subscriptions.
func (s *Service) Subscriptions(ctx context.Context, subscriber string) ([]core.PushSubscription, error) {
	return s.store.ListPushSubscriptions(ctx, subscriber)
}

// Delivery is the outcome of sending to one subscription.
type Delivery struct {
	SubscriptionID string `json:"subscription_id"`
	Label          string `json:"label,omitempty"`
	Delivered      bool   `json:"delivered"`
	// Removed means the push service said the subscription is gone.
	Removed bool   `json:"removed,omitempty"`
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SendTest sends a test notification now to one of subscriber's
// subscriptions, or to all of them when id is empty.
func (s *Service) SendTest(ctx context.Context, subscriber, id string) []Delivery {
	event := TestEvent(time.Now().UTC())
	return s.deliver(ctx, Batch{Kind: "test", Events: []Event{event}, At: event.At}, subscriber, id)
}

// deliver sends batch to every subscription of subscriber that wants its
// kind (a test goes to all, or just to onlyID).
func (s *Service) deliver(ctx context.Context, batch Batch, subscriber, onlyID string) []Delivery {
	subs, err := s.store.ListPushSubscriptions(ctx, subscriber)
	if err != nil {
		s.log.Warn("push: list subscriptions failed", "err", err)
		return nil
	}
	test := batch.Kind == "test"
	var results []Delivery
	for _, sub := range subs {
		if onlyID != "" && sub.ID != onlyID {
			continue
		}
		if !test && !sub.Wants(batch.Kind) {
			continue
		}
		results = append(results, s.sendOne(ctx, sub, batch))
	}
	return results
}

func (s *Service) sendOne(ctx context.Context, sub core.PushSubscription, batch Batch) Delivery {
	result := Delivery{SubscriptionID: sub.ID, Label: sub.Label}
	payload, err := json.Marshal(batch.Render(sub.Lang))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	status, err := s.sender.Send(sendCtx, sub, payload, optionsFor(batch))
	cancel()
	result.Status = status
	now := time.Now().UTC()
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelRecord()
	switch {
	case err == nil && status >= 200 && status < 300:
		result.Delivered = true
		_ = s.store.RecordPushDelivery(recordCtx, sub.ID, now, "")
	case status == http.StatusNotFound || status == http.StatusGone:
		// The browser unsubscribed or the subscription expired; it will
		// never work again.
		result.Removed = true
		if _, err := s.store.DeletePushSubscription(recordCtx, sub.ID, ""); err != nil {
			s.log.Warn("push: remove expired subscription failed", "subscription", sub.ID, "err", err)
		}
	default:
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = fmt.Sprintf("the push service answered %d", status)
		}
		_ = s.store.RecordPushDelivery(recordCtx, sub.ID, now, result.Error)
		s.log.Warn("push: delivery failed", "subscription", sub.ID, "status", status, "err", result.Error)
	}
	return result
}

func optionsFor(batch Batch) SendOptions {
	opts := SendOptions{TTL: 3600, Urgency: "normal"}
	switch batch.Kind {
	case core.PushKindWaiting:
		// Approvals expire on their own after about ten minutes.
		opts.TTL, opts.Urgency = 600, "high"
	case core.PushKindTickets:
		opts.TTL = 24 * 3600
	case "test":
		opts.TTL, opts.Urgency = 120, "high"
	}
	tag := batch.Render("en").Tag
	if tag != "" {
		sum := sha256.Sum256([]byte(tag))
		opts.Topic = base64.RawURLEncoding.EncodeToString(sum[:])[:32]
	}
	return opts
}

func decodeKey(value string) ([]byte, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "=")
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

// webPushSender sends through the push service named in each endpoint.
type webPushSender struct {
	keys    core.PushVAPIDKeys
	subject string
	client  *http.Client
}

// NewWebPushSender signs with keys. subject is the VAPID contact; client
// defaults to one that honours the proxy environment.
func NewWebPushSender(keys core.PushVAPIDKeys, subject string, client *http.Client) Sender {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = DefaultSubject
	}
	// The library adds "mailto:" to anything that is not an https URL.
	subject = strings.TrimPrefix(subject, "mailto:")
	if client == nil {
		client = &http.Client{Timeout: sendTimeout}
	}
	return &webPushSender{keys: keys, subject: subject, client: client}
}

func (w *webPushSender) Send(ctx context.Context, sub core.PushSubscription, payload []byte, opts SendOptions) (int, error) {
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient: w.client, Subscriber: w.subject,
		VAPIDPublicKey: w.keys.PublicKey, VAPIDPrivateKey: w.keys.PrivateKey,
		TTL: opts.TTL, Urgency: webpush.Urgency(opts.Urgency), Topic: opts.Topic,
	})
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, nil
}
