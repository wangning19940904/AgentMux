// Package eventrelay delivers durable events to authenticated local subscribers.
package eventrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

const IngestionTimeout = 500 * time.Millisecond
const SignatureHeader = "X-AgentMux-Signature"
const TimestampHeader = "X-AgentMux-Timestamp"
const DeliveryHeader = "X-AgentMux-Delivery-ID"
const EventHeader = "X-AgentMux-Event-ID"

var eventName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,199}$`)

func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func Signature(secret, timestamp, deliveryID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + deliveryID + "."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}
func VerifySignature(secret, timestamp, deliveryID, signature string, body []byte, now time.Time) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || deliveryID == "" || secret == "" {
		return false
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age < -5*time.Minute || age > 5*time.Minute {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(Signature(secret, timestamp, deliveryID, body)))
}

func ValidateCallback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid callback URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return fmt.Errorf("callback must be an HTTP(S) loopback URL without credentials or fragment")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("callback host must be localhost or a loopback IP")
		}
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid callback port")
		}
	}
	return nil
}

// LocalHTTPClient enforces loopback on every dial. Proxies and redirects are
// disabled even when the daemon inherits a user's shell proxy settings.
func LocalHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var addresses []net.IPAddr
		if host == "localhost" {
			addresses, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		} else {
			addresses = []net.IPAddr{{IP: net.ParseIP(host)}}
		}
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("callback host has no addresses")
		}
		for _, ip := range addresses {
			if ip.IP == nil || !ip.IP.IsLoopback() || ip.Zone != "" {
				return nil, fmt.Errorf("callback resolved outside loopback")
			}
		}
		var last error
		for _, ip := range addresses {
			conn, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func ValidateSubscription(sub *core.EventSubscription) error {
	sub.Key = strings.TrimSpace(sub.Key)
	sub.Name = strings.TrimSpace(sub.Name)
	sub.CallbackURL = strings.TrimSpace(sub.CallbackURL)
	if !eventName.MatchString(sub.Key) || sub.Name == "" || len(sub.Name) > 200 {
		return fmt.Errorf("key must be a stable identifier and name must contain 1–200 bytes")
	}
	if sub.Source != "agentmux" && sub.Source != "feishu" && sub.Source != "lark" {
		return fmt.Errorf("source must be agentmux, feishu or lark")
	}
	if len(sub.SourceRefs) == 0 || len(sub.SourceRefs) > 100 || len(sub.EventTypes) == 0 || len(sub.EventTypes) > 100 {
		return fmt.Errorf("source_refs and event_types must contain 1–100 items")
	}
	for _, ref := range sub.SourceRefs {
		kind, id, ok := strings.Cut(ref, ":")
		if ref != "system" && (!ok || id == "" || !slices.Contains([]string{"channel", "agent", "trigger"}, kind)) {
			return fmt.Errorf("invalid source_ref %q", ref)
		}
		if sub.Source != "agentmux" && kind != "channel" {
			return fmt.Errorf("platform events require channel sources")
		}
	}
	for _, typ := range sub.EventTypes {
		if !eventName.MatchString(typ) || typ == "app_ticket" {
			return fmt.Errorf("invalid or reserved event type %q", typ)
		}
		if sub.Source == "agentmux" && !slices.Contains(core.LifecycleEventTypes(), typ) {
			return fmt.Errorf("unknown lifecycle event %q", typ)
		}
	}
	for k, values := range sub.Filters {
		if !slices.Contains([]string{"chat_id", "file_token", "table_id", "agent_id"}, k) || len(values) == 0 || len(values) > 100 {
			return fmt.Errorf("invalid filter %q", k)
		}
		for _, v := range values {
			if v == "" || len(v) > 256 {
				return fmt.Errorf("invalid filter value")
			}
		}
	}
	return ValidateCallback(sub.CallbackURL)
}

type Service struct {
	store       *store.Store
	log         *slog.Logger
	client      *http.Client
	mu          sync.RWMutex
	types       map[string][]string
	health      map[string]core.EventIngestionHealth
	unsubscribe func()
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	once        sync.Once
}

func New(st *store.Store, log *slog.Logger, engine *core.Engine) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{store: st, log: log, client: LocalHTTPClient(), types: map[string][]string{}, health: map[string]core.EventIngestionHealth{}}
	if engine != nil {
		engine.ConfigureEventIngress(func(ch core.Channel) core.PlatformEventIngress {
			return core.PlatformEventIngress{Publish: func(ctx context.Context, e core.RelayEvent) error {
				e.Source = ch.Type
				e.SourceRef = "channel:" + ch.ID
				if e.Attributes == nil {
					e.Attributes = map[string]string{}
				}
				e.Attributes["app_id"] = ch.Config["app_id"]
				e.Attributes["channel_id"] = ch.ID
				e.Attributes["agent_id"] = ch.AgentID
				return s.Publish(ctx, e)
			}, EventTypes: func() []string { return s.EventTypes("channel:"+ch.ID, ch.Type) }}
		})
		s.unsubscribe = engine.SubscribeEventSink(func(kind core.HookEvent, data map[string]string) {
			if data["agent_id"] == "" && strings.HasPrefix(data["project"], "agent:") {
				data["agent_id"] = strings.TrimPrefix(data["project"], "agent:")
			}
			payload, _ := json.Marshal(data)
			_ = s.Publish(context.Background(), core.RelayEvent{Source: "agentmux", Type: string(kind), SourceRef: core.LifecycleSourceRef(data), Attributes: data, Data: payload})
		})
	}
	return s
}
func (s *Service) Publish(ctx context.Context, e core.RelayEvent) error { return s.publish(ctx, e, "") }
func (s *Service) publish(ctx context.Context, e core.RelayEvent, target string) error {
	ctx, cancel := context.WithTimeout(ctx, IngestionTimeout)
	defer cancel()
	now := time.Now().UTC()
	e.SchemaVersion = "1"
	if e.ID == "" {
		e.ID = "evt_" + uuid.NewString()
	}
	e.ReceivedAt = now
	if e.OccurredAt.IsZero() {
		e.OccurredAt = now
	}
	err := s.store.IngestRelayEvent(ctx, e, target)
	s.mu.Lock()
	health := s.health[e.SourceRef]
	if err != nil {
		health.Failures++
		health.LastFailureAt = &now
		health.LastError = "event persistence failed"
	} else {
		health.LastSuccessAt = &now
		health.LastError = ""
	}
	s.health[e.SourceRef] = health
	s.mu.Unlock()
	if err != nil {
		s.log.Error("event relay ingestion failed", "source_ref", e.SourceRef, "type", e.Type, "err", err)
	}
	return err
}
func (s *Service) Test(ctx context.Context, sub core.EventSubscription) error {
	body, _ := json.Marshal(map[string]any{"test": true, "message": "AgentMux callback verification"})
	return s.publish(ctx, core.RelayEvent{Source: sub.Source, Type: "agentmux.subscription.test", SourceRef: sub.SourceRefs[0], Attributes: map[string]string{"test": "true"}, Data: body}, sub.ID)
}
func (s *Service) Health(ref string) core.EventIngestionHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.health[ref]
}
func (s *Service) EventTypes(ref, source string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.types[source+"|"+ref])
}
func (s *Service) Refresh(ctx context.Context) error {
	subs, err := s.store.ListEventSubscriptions(ctx, nil)
	if err != nil {
		return err
	}
	types := map[string][]string{}
	for _, sub := range subs {
		if sub.Source == "agentmux" {
			continue
		}
		for _, ref := range sub.SourceRefs {
			ok, err := s.store.EventSourceAllowed(ctx, sub.OwnerTenantID, ref)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			key := sub.Source + "|" + ref
			types[key] = append(types[key], sub.EventTypes...)
		}
	}
	for key, list := range types {
		slices.Sort(list)
		types[key] = slices.Compact(list)
	}
	s.mu.Lock()
	s.types = types
	s.mu.Unlock()
	return nil
}
func (s *Service) Start(parent context.Context) error {
	if err := s.Refresh(parent); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	for range 8 {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.worker(ctx) }()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		lastPrune := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				opCtx, done := context.WithTimeout(ctx, 10*time.Second)
				if err := s.Refresh(opCtx); err != nil && ctx.Err() == nil {
					s.log.Error("event relay subscription refresh failed", "err", err)
				}
				if time.Since(lastPrune) > time.Hour {
					if err := s.store.PruneEventDeliveries(opCtx, time.Now()); err != nil && ctx.Err() == nil {
						s.log.Error("event relay retention failed", "err", err)
					}
					lastPrune = time.Now()
				}
				done()
			}
		}
	}()
	return nil
}
func (s *Service) Stop() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.unsubscribe != nil {
			s.unsubscribe()
		}
		s.wg.Wait()
		s.client.CloseIdleConnections()
	})
}
func (s *Service) worker(ctx context.Context) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			d, err := s.store.ClaimEventDelivery(opCtx, time.Now().UTC())
			if err == nil && d != nil {
				s.deliver(opCtx, d)
			} else if err != nil && ctx.Err() == nil {
				s.log.Error("event relay claim failed", "err", err)
			}
			cancel()
		}
	}
}
func RetryDelay(attempt int) time.Duration {
	delay := time.Second * time.Duration(1<<min(max(attempt-1, 0), 9))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return time.Duration(float64(delay) * (0.8 + 0.2*mathrand.Float64()))
}
func (s *Service) deliver(ctx context.Context, d *core.EventDelivery) {
	sub, err := s.store.GetEventSubscription(ctx, d.SubscriptionID)
	state, message := "blocked", "subscription unavailable"
	if err == nil && sub != nil && !sub.Deleted && !sub.Paused {
		var allowed bool
		allowed, err = s.store.EventSourceAllowed(ctx, sub.OwnerTenantID, d.Event.SourceRef)
		if err == nil && allowed {
			err = s.send(ctx, d, sub)
			if err != nil {
				s.log.Error("event relay completion failed", "delivery_id", d.ID, "err", err)
			}
			return
		}
		message = "event source permission unavailable"
	} else if sub != nil && sub.Deleted {
		state = "cancelled"
		message = "subscription deleted"
	} else if sub != nil && sub.Paused {
		state = "pending"
		message = "subscription paused"
	}
	if err := s.store.FinishEventDelivery(ctx, d, "", state, 0, message, time.Now().Add(10*time.Second)); err != nil {
		s.log.Error("event relay release failed", "delivery_id", d.ID, "err", err)
	}
}
func (s *Service) send(ctx context.Context, d *core.EventDelivery, sub *core.EventSubscription) error {
	now := time.Now().UTC()
	attempt, err := s.store.BeginEventAttempt(ctx, d, now)
	if err != nil {
		return err
	}
	status := 0
	message := ""
	state := "sent"
	body, err := json.Marshal(d.Event)
	if err == nil {
		err = ValidateCallback(sub.CallbackURL)
	}
	if err == nil {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, sub.CallbackURL, bytes.NewReader(body))
		if err == nil {
			timestamp := strconv.FormatInt(now.Unix(), 10)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(EventHeader, d.EventID)
			req.Header.Set(DeliveryHeader, d.ID)
			req.Header.Set(TimestampHeader, timestamp)
			req.Header.Set(SignatureHeader, Signature(sub.Secret, timestamp, d.ID, body))
			var response *http.Response
			response, err = s.client.Do(req)
			if response != nil {
				status = response.StatusCode
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
			}
			if err == nil && (status < 200 || status >= 300) {
				err = fmt.Errorf("callback returned HTTP %d", status)
			}
		}
	}
	if err != nil {
		// Do not store arbitrary response bodies or URLs (which may contain secrets).
		if status != 0 {
			message = fmt.Sprintf("callback returned HTTP %d", status)
		} else if errors.Is(err, context.DeadlineExceeded) {
			message = "callback timed out"
		} else {
			message = "callback connection or request failed"
		}
		state = "retry"
		if time.Since(*d.FirstAttemptAt) >= 24*time.Hour {
			state = "dead"
		}
	}
	return s.store.FinishEventDelivery(ctx, d, attempt, state, status, message, time.Now().Add(RetryDelay(d.Attempts)))
}
