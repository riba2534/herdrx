package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/riba2534/herdrx/internal/store"
)

type keysFile struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

type Service struct {
	private string
	public  string
	client  webpush.HTTPClient
}

type Notification struct {
	Title string         `json:"title"`
	Body  string         `json:"body"`
	Tag   string         `json:"tag"`
	Data  map[string]any `json:"data"`
}

func Open(dataDir string) (*Service, error) {
	return OpenWithClient(dataDir, newHTTPClient())
}

// OpenWithClient allows a trusted embedding application to provide its own
// outbound transport. The website always uses Open's public-address policy.
func OpenWithClient(dataDir string, client webpush.HTTPClient) (*Service, error) {
	if client == nil {
		return nil, fmt.Errorf("push HTTP client is required")
	}
	path := filepath.Join(dataDir, "vapid.json")
	encoded, err := os.ReadFile(path)
	if err == nil {
		var keys keysFile
		if json.Unmarshal(encoded, &keys) != nil || keys.Private == "" || keys.Public == "" {
			return nil, fmt.Errorf("invalid VAPID key file")
		}
		return &Service{private: keys.Private, public: keys.Public, client: client}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("generate VAPID keys: %w", err)
	}
	encoded, _ = json.MarshalIndent(keysFile{Private: privateKey, Public: publicKey}, "", "  ")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return &Service{private: privateKey, public: publicKey, client: client}, nil
}

func (s *Service) PublicKey() string { return s.public }

func (s *Service) Close() {
	if client, ok := s.client.(interface{ CloseIdleConnections() }); ok {
		client.CloseIdleConnections()
	}
}

func (s *Service) Notify(ctx context.Context, subscription store.PushSubscription, notification Notification) (expired bool, err error) {
	if err := ValidateSubscription(subscription.Endpoint, subscription.P256DH, subscription.Auth); err != nil {
		return true, err
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return false, err
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys:     webpush.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth},
	}, &webpush.Options{
		HTTPClient: s.client,
		Subscriber: "mailto:admin@localhost", VAPIDPublicKey: s.public, VAPIDPrivateKey: s.private,
		TTL: 300, Urgency: webpush.UrgencyHigh, VapidExpiration: time.Now().Add(6 * time.Hour),
	})
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			// Provider URLs contain bearer-like subscription tokens. Keep them
			// out of application logs while preserving cancellation/error identity.
			return false, fmt.Errorf("push delivery failed: %w", requestError.Err)
		}
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 || response.StatusCode == 410 {
		return true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("push endpoint returned status %d", response.StatusCode)
	}
	return false, nil
}
