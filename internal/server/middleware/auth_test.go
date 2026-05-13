package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/golang-jwt/jwt/v5"
)

type mockHTTPTransport struct {
	request   *http.Request
	headers   mockHeader
	reply     mockHeader
	operation string
}

type mockHeader struct {
	values http.Header
}

func (m *mockHTTPTransport) Kind() transport.Kind {
	return transport.KindHTTP
}

func (m *mockHTTPTransport) Endpoint() string {
	return "http://127.0.0.1:8000"
}

func (m *mockHTTPTransport) Operation() string {
	return m.operation
}

func (m *mockHTTPTransport) RequestHeader() transport.Header {
	return m.headers
}

func (m *mockHTTPTransport) ReplyHeader() transport.Header {
	return m.reply
}

func (m *mockHTTPTransport) Request() *http.Request {
	return m.request
}

func (m *mockHTTPTransport) PathTemplate() string {
	return "/test"
}

var _ kratoshttp.Transporter = (*mockHTTPTransport)(nil)

func (h mockHeader) Get(key string) string {
	return h.values.Get(key)
}

func (h mockHeader) Set(key string, value string) {
	h.values.Set(key, value)
}

func (h mockHeader) Add(key string, value string) {
	h.values.Add(key, value)
}

func (h mockHeader) Keys() []string {
	keys := make([]string, 0, len(h.values))
	for key := range h.values {
		keys = append(keys, key)
	}
	return keys
}

func (h mockHeader) Values(key string) []string {
	return h.values.Values(key)
}

func TestAuthMiddlewareJWTSuccess(t *testing.T) {
	now := time.Unix(1700000000, 0)
	token := signJWT(t, jwt.MapClaims{
		"user_id":   "user-1",
		"device_id": "ios",
		"iss":       "big-market",
		"aud":       []string{"frontend"},
		"exp":       now.Add(5 * time.Minute).Unix(),
	}, []byte("secret"))

	tr := newMockTransport(t)
	tr.headers.Set("Authorization", "Bearer "+token)
	tr.headers.Set("X-Device-Id", "ios")

	reply, err := runAuthMiddleware(t, tr, AuthOptions{
		Now: func() time.Time { return now },
		JWT: JWTOptions{
			Enabled:        true,
			SigningKey:     []byte("secret"),
			SigningMethods: []string{"HS256"},
			Issuer:         "big-market",
			Audience:       []string{"frontend"},
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	info, ok := reply.(AuthInfo)
	if !ok {
		t.Fatalf("expected auth info reply, got %T", reply)
	}
	if info.UserID != "user-1" {
		t.Fatalf("expected user-1, got %s", info.UserID)
	}
	if info.Source != AuthSourceJWT {
		t.Fatalf("expected jwt source, got %s", info.Source)
	}
}

func TestAuthMiddlewareJWTExpiredReturnsUnauthorized(t *testing.T) {
	now := time.Unix(1700000000, 0)
	token := signJWT(t, jwt.MapClaims{
		"user_id":   "user-1",
		"device_id": "ios",
		"iss":       "big-market",
		"aud":       []string{"frontend"},
		"exp":       now.Add(-time.Minute).Unix(),
	}, []byte("secret"))

	tr := newMockTransport(t)
	tr.headers.Set("Authorization", "Bearer "+token)
	tr.headers.Set("X-Device-Id", "ios")

	_, err := runAuthMiddleware(t, tr, AuthOptions{
		Now: func() time.Time { return now },
		JWT: JWTOptions{
			Enabled:          true,
			SigningKey:       []byte("secret"),
			SigningMethods:   []string{"HS256"},
			Issuer:           "big-market",
			Audience:         []string{"frontend"},
			RequireExpiresAt: true,
		},
	})
	if err == nil {
		t.Fatal("expected unauthorized error, got nil")
	}
	if !kerrors.IsUnauthorized(err) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestAuthMiddlewareSessionDeviceMismatchReturnsUnauthorized(t *testing.T) {
	now := time.Unix(1700000000, 0)
	tr := newMockTransport(t)
	tr.headers.Set("X-Session-Id", "session-1")
	tr.headers.Set("X-Device-Id", "android")

	_, err := runAuthMiddleware(t, tr, AuthOptions{
		Now: func() time.Time { return now },
		Session: SessionOptions{
			Enabled: true,
			Validator: func(_ context.Context, req SessionValidateRequest) (*AuthInfo, error) {
				if req.SessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", req.SessionID)
				}
				return &AuthInfo{
					UserID:    "user-1",
					SessionID: "session-1",
					DeviceID:  "ios",
					Issuer:    "big-market",
					Audience:  []string{"frontend"},
					ExpiresAt: now.Add(5 * time.Minute),
				}, nil
			},
			Issuer:    "big-market",
			Audience:  []string{"frontend"},
			ClockSkew: 0,
		},
	})
	if err == nil {
		t.Fatal("expected unauthorized error, got nil")
	}
	if !kerrors.IsUnauthorized(err) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestAuthMiddlewareGatewaySuccess(t *testing.T) {
	now := time.Unix(1700000000, 0)
	secret := []byte("gateway-secret")
	tr := newMockTransport(t)
	tr.headers.Set("X-User-Id", "user-2")
	tr.headers.Set("X-Session-Id", "session-2")
	tr.headers.Set("X-Device-Id", "android")
	tr.headers.Set("X-Auth-Issuer", "big-market-gateway")
	tr.headers.Set("X-Auth-Audience", "big-market")
	tr.headers.Set("X-Auth-Expires-At", "1700000300")
	tr.headers.Set("X-Auth-Timestamp", "1700000000")
	tr.headers.Set("X-Auth-Nonce", "nonce-1")

	info := AuthInfo{
		UserID:    "user-2",
		SessionID: "session-2",
		DeviceID:  "android",
		Issuer:    "big-market-gateway",
		Audience:  []string{"big-market"},
		ExpiresAt: time.Unix(1700000300, 0),
	}
	signature := signGateway(buildGatewayCanonicalString(tr.headers, info, GatewayOptions{}.withDefaults()), secret)
	tr.headers.Set("X-Auth-Signature", signature)

	reply, err := runAuthMiddleware(t, tr, AuthOptions{
		Now: func() time.Time { return now },
		Gateway: GatewayOptions{
			Enabled:         true,
			SignatureSecret: secret,
			Issuer:          "big-market-gateway",
			Audience:        []string{"big-market"},
			ClockSkew:       0,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	authInfo := reply.(AuthInfo)
	if authInfo.UserID != "user-2" {
		t.Fatalf("expected user-2, got %s", authInfo.UserID)
	}
	if authInfo.Source != AuthSourceGateway {
		t.Fatalf("expected gateway source, got %s", authInfo.Source)
	}
}

func newMockTransport(t *testing.T) *mockHTTPTransport {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/test", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	return &mockHTTPTransport{
		request:   req,
		headers:   mockHeader{values: http.Header{}},
		reply:     mockHeader{values: http.Header{}},
		operation: "/test.Operation",
	}
}

func runAuthMiddleware(t *testing.T, tr transport.Transporter, opts AuthOptions) (any, error) {
	t.Helper()
	ctx := transport.NewServerContext(context.Background(), tr)
	next := func(ctx context.Context, _ any) (any, error) {
		info, ok := AuthInfoFromContext(ctx)
		if !ok {
			t.Fatal("auth info was not injected into context")
		}
		return info, nil
	}
	handler := AuthMiddleware(opts)(middleware.Handler(next))
	return handler(ctx, nil)
}

func signJWT(t *testing.T, claims jwt.MapClaims, secret []byte) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("failed to sign jwt: %v", err)
	}
	return signed
}

func signGateway(canonical string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}
