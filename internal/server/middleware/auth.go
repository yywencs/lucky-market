package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/golang-jwt/jwt/v5"
)

type authContextKey struct{}

type AuthSource string

const (
	AuthSourceGateway AuthSource = "gateway"
	AuthSourceJWT     AuthSource = "jwt"
	AuthSourceSession AuthSource = "session"
)

const (
	defaultAuthorizationHeader = "Authorization"
	defaultBearerPrefix        = "Bearer "
	defaultDeviceHeader        = "X-Device-Id"
	defaultSessionHeader       = "X-Session-Id"
	defaultGatewayUserHeader   = "X-User-Id"
	defaultGatewayIssuerHeader = "X-Auth-Issuer"
	defaultGatewayAudience     = "X-Auth-Audience"
	defaultGatewayExpireHeader = "X-Auth-Expires-At"
	defaultGatewaySignHeader   = "X-Auth-Signature"
	defaultGatewayTimeHeader   = "X-Auth-Timestamp"
	defaultGatewayNonceHeader  = "X-Auth-Nonce"
)

// AuthInfo 是统一的鉴权结果，便于业务层从 context 中读取登录态。
type AuthInfo struct {
	UserID    string
	SessionID string
	DeviceID  string
	Issuer    string
	Audience  []string
	Source    AuthSource
	Subject   string
	ExpiresAt time.Time
	TokenID   string
	RawToken  string
}

type SessionValidateRequest struct {
	SessionID string
	DeviceID  string
	Transport transport.Transporter
}

type GatewaySignatureRequest struct {
	Canonical string
	Signature string
	Header    transport.Header
	AuthInfo  AuthInfo
}

type SessionValidator func(ctx context.Context, req SessionValidateRequest) (*AuthInfo, error)

type GatewaySignatureValidator func(ctx context.Context, req GatewaySignatureRequest) error

type AuthOptions struct {
	Skipper func(ctx context.Context, tr transport.Transporter) bool
	Now     func() time.Time
	Sources []AuthSource
	JWT     JWTOptions
	Session SessionOptions
	Gateway GatewayOptions
}

type JWTOptions struct {
	Enabled          bool
	Header           string
	Prefix           string
	CookieNames      []string
	KeyFunc          jwt.Keyfunc
	SigningKey       any
	SigningMethods   []string
	Issuer           string
	Audience         []string
	DeviceHeader     string
	DeviceClaim      string
	UserIDClaims     []string
	SessionIDClaim   string
	RequireExpiresAt bool
	ClockSkew        time.Duration
}

type SessionOptions struct {
	Enabled        bool
	HeaderNames    []string
	CookieNames    []string
	DeviceHeader   string
	Validator      SessionValidator
	Issuer         string
	Audience       []string
	ClockSkew      time.Duration
	RequireDevice  bool
	RequireExpires bool
}

type GatewayOptions struct {
	Enabled            bool
	UserIDHeader       string
	SessionHeader      string
	DeviceHeader       string
	IssuerHeader       string
	AudienceHeader     string
	ExpireHeader       string
	SignatureHeader    string
	TimestampHeader    string
	NonceHeader        string
	SignatureSecret    []byte
	SignatureValidator GatewaySignatureValidator
	Issuer             string
	Audience           []string
	ClockSkew          time.Duration
	RequireDevice      bool
	RequireExpires     bool
}

func AuthMiddleware(opts AuthOptions) middleware.Middleware {
	opts = opts.withDefaults()
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			tr, ok := transport.FromServerContext(ctx)
			if !ok || tr == nil {
				return nil, unauthorizedError("missing transport context")
			}
			if opts.Skipper != nil && opts.Skipper(ctx, tr) {
				return handler(ctx, req)
			}

			authInfo, err := authenticate(ctx, tr, opts)
			if err != nil {
				return nil, unauthorizedError(err.Error())
			}

			return handler(NewAuthContext(ctx, *authInfo), req)
		}
	}
}

func NewAuthContext(ctx context.Context, info AuthInfo) context.Context {
	return context.WithValue(ctx, authContextKey{}, info)
}

func AuthInfoFromContext(ctx context.Context) (AuthInfo, bool) {
	info, ok := ctx.Value(authContextKey{}).(AuthInfo)
	return info, ok
}

func MatchOperationSkipper(operations ...string) func(context.Context, transport.Transporter) bool {
	allowed := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		if operation == "" {
			continue
		}
		allowed[operation] = struct{}{}
	}
	return func(_ context.Context, tr transport.Transporter) bool {
		_, ok := allowed[tr.Operation()]
		return ok
	}
}

func authenticate(ctx context.Context, tr transport.Transporter, opts AuthOptions) (*AuthInfo, error) {
	var lastErr error

	for _, source := range opts.Sources {
		switch source {
		case AuthSourceGateway:
			info, attempted, err := authenticateGateway(ctx, tr, opts)
			if attempted {
				if err != nil {
					return nil, err
				}
				return info, nil
			}
			lastErr = errOrLast(err, lastErr)
		case AuthSourceJWT:
			info, attempted, err := authenticateJWT(ctx, tr, opts)
			if attempted {
				if err != nil {
					return nil, err
				}
				return info, nil
			}
			lastErr = errOrLast(err, lastErr)
		case AuthSourceSession:
			info, attempted, err := authenticateSession(ctx, tr, opts)
			if attempted {
				if err != nil {
					return nil, err
				}
				return info, nil
			}
			lastErr = errOrLast(err, lastErr)
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("missing authentication credentials")
}

func authenticateJWT(_ context.Context, tr transport.Transporter, opts AuthOptions) (*AuthInfo, bool, error) {
	cfg := opts.JWT
	if !cfg.Enabled {
		return nil, false, nil
	}

	rawToken, attempted := extractJWTToken(tr, cfg)
	if !attempted {
		return nil, false, nil
	}

	keyFunc := cfg.KeyFunc
	if keyFunc == nil {
		if cfg.SigningKey == nil {
			return nil, true, fmt.Errorf("jwt signing key is not configured")
		}
		keyFunc = func(token *jwt.Token) (any, error) {
			if len(cfg.SigningMethods) > 0 && !containsString(cfg.SigningMethods, token.Method.Alg()) {
				return nil, fmt.Errorf("jwt signing method %s is not allowed", token.Method.Alg())
			}
			return cfg.SigningKey, nil
		}
	}

	parserOpts := []jwt.ParserOption{
		jwt.WithTimeFunc(opts.Now),
	}
	if len(cfg.SigningMethods) > 0 {
		parserOpts = append(parserOpts, jwt.WithValidMethods(cfg.SigningMethods))
	}
	if cfg.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(cfg.Issuer))
	}
	if len(cfg.Audience) > 0 {
		parserOpts = append(parserOpts, jwt.WithAudience(cfg.Audience...))
	}
	if cfg.ClockSkew > 0 {
		parserOpts = append(parserOpts, jwt.WithLeeway(cfg.ClockSkew))
	}
	if cfg.RequireExpiresAt {
		parserOpts = append(parserOpts, jwt.WithExpirationRequired())
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(rawToken, claims, keyFunc, parserOpts...)
	if err != nil {
		return nil, true, fmt.Errorf("jwt validation failed: %w", err)
	}
	if token == nil || !token.Valid {
		return nil, true, fmt.Errorf("jwt token is invalid")
	}

	deviceID, err := extractRequiredStringClaim(claims, cfg.DeviceClaim)
	if err != nil {
		return nil, true, err
	}
	if deviceErr := validateDeviceHeader(tr.RequestHeader(), cfg.DeviceHeader, deviceID); deviceErr != nil {
		return nil, true, deviceErr
	}

	userID, err := extractUserIDClaim(claims, cfg.UserIDClaims)
	if err != nil {
		return nil, true, err
	}

	info := &AuthInfo{
		UserID:    userID,
		SessionID: readStringClaim(claims, cfg.SessionIDClaim),
		DeviceID:  deviceID,
		Issuer:    readStringClaim(claims, "iss"),
		Audience:  normalizeAudienceClaim(claims["aud"]),
		Source:    AuthSourceJWT,
		Subject:   readStringClaim(claims, "sub"),
		ExpiresAt: readTimeClaim(claims["exp"]),
		TokenID:   readStringClaim(claims, "jti"),
		RawToken:  rawToken,
	}

	return info, true, nil
}

func authenticateSession(ctx context.Context, tr transport.Transporter, opts AuthOptions) (*AuthInfo, bool, error) {
	cfg := opts.Session
	if !cfg.Enabled {
		return nil, false, nil
	}

	sessionID, attempted := extractSessionID(tr, cfg)
	if !attempted {
		return nil, false, nil
	}
	if cfg.Validator == nil {
		return nil, true, fmt.Errorf("session validator is not configured")
	}

	deviceID := strings.TrimSpace(tr.RequestHeader().Get(cfg.DeviceHeader))
	info, err := cfg.Validator(ctx, SessionValidateRequest{
		SessionID: sessionID,
		DeviceID:  deviceID,
		Transport: tr,
	})
	if err != nil {
		return nil, true, fmt.Errorf("session validation failed: %w", err)
	}
	if info == nil {
		return nil, true, fmt.Errorf("session validation returned empty auth info")
	}

	if info.UserID == "" {
		return nil, true, fmt.Errorf("session user id is empty")
	}
	if cfg.RequireDevice && strings.TrimSpace(info.DeviceID) == "" {
		return nil, true, fmt.Errorf("session device information is missing")
	}
	if err := validateOptionalDeviceHeader(tr.RequestHeader(), cfg.DeviceHeader, info.DeviceID); err != nil {
		return nil, true, err
	}
	if err := validateIssuer(info.Issuer, cfg.Issuer); err != nil {
		return nil, true, err
	}
	if err := validateAudience(info.Audience, cfg.Audience); err != nil {
		return nil, true, err
	}
	if err := validateExpiration(info.ExpiresAt, cfg.RequireExpires, cfg.ClockSkew, opts.Now()); err != nil {
		return nil, true, err
	}

	info.Source = AuthSourceSession
	info.SessionID = sessionID
	return info, true, nil
}

func authenticateGateway(ctx context.Context, tr transport.Transporter, opts AuthOptions) (*AuthInfo, bool, error) {
	cfg := opts.Gateway
	if !cfg.Enabled {
		return nil, false, nil
	}

	header := tr.RequestHeader()
	userID := strings.TrimSpace(header.Get(cfg.UserIDHeader))
	signature := strings.TrimSpace(header.Get(cfg.SignatureHeader))
	if userID == "" && signature == "" {
		return nil, false, nil
	}
	if userID == "" {
		return nil, true, fmt.Errorf("gateway user id header is empty")
	}
	if signature == "" {
		return nil, true, fmt.Errorf("gateway signature header is empty")
	}

	info := AuthInfo{
		UserID:    userID,
		SessionID: strings.TrimSpace(header.Get(cfg.SessionHeader)),
		DeviceID:  strings.TrimSpace(header.Get(cfg.DeviceHeader)),
		Issuer:    strings.TrimSpace(header.Get(cfg.IssuerHeader)),
		Audience:  splitAndTrim(header.Get(cfg.AudienceHeader), ","),
		Source:    AuthSourceGateway,
		ExpiresAt: parseHeaderTime(header.Get(cfg.ExpireHeader)),
	}

	if cfg.RequireDevice && info.DeviceID == "" {
		return nil, true, fmt.Errorf("gateway device header is empty")
	}
	if err := validateIssuer(info.Issuer, cfg.Issuer); err != nil {
		return nil, true, err
	}
	if err := validateAudience(info.Audience, cfg.Audience); err != nil {
		return nil, true, err
	}
	if err := validateExpiration(info.ExpiresAt, cfg.RequireExpires, cfg.ClockSkew, opts.Now()); err != nil {
		return nil, true, err
	}

	canonical := buildGatewayCanonicalString(header, info, cfg)
	req := GatewaySignatureRequest{
		Canonical: canonical,
		Signature: signature,
		Header:    header,
		AuthInfo:  info,
	}

	if cfg.SignatureValidator != nil {
		if err := cfg.SignatureValidator(ctx, req); err != nil {
			return nil, true, fmt.Errorf("gateway signature validation failed: %w", err)
		}
	} else {
		if len(cfg.SignatureSecret) == 0 {
			return nil, true, fmt.Errorf("gateway signature secret is not configured")
		}
		if err := verifyGatewayHMACSignature(req.Canonical, req.Signature, cfg.SignatureSecret); err != nil {
			return nil, true, err
		}
	}

	return &info, true, nil
}

func (o AuthOptions) withDefaults() AuthOptions {
	if o.Now == nil {
		o.Now = time.Now
	}
	if len(o.Sources) == 0 {
		o.Sources = []AuthSource{AuthSourceGateway, AuthSourceJWT, AuthSourceSession}
	}
	o.JWT = o.JWT.withDefaults()
	o.Session = o.Session.withDefaults()
	o.Gateway = o.Gateway.withDefaults()
	return o
}

func (o JWTOptions) withDefaults() JWTOptions {
	if o.Header == "" {
		o.Header = defaultAuthorizationHeader
	}
	if o.Prefix == "" {
		o.Prefix = defaultBearerPrefix
	}
	if o.DeviceHeader == "" {
		o.DeviceHeader = defaultDeviceHeader
	}
	if o.DeviceClaim == "" {
		o.DeviceClaim = "device_id"
	}
	if len(o.UserIDClaims) == 0 {
		o.UserIDClaims = []string{"user_id", "uid", "sub"}
	}
	if o.SessionIDClaim == "" {
		o.SessionIDClaim = "sid"
	}
	return o
}

func (o SessionOptions) withDefaults() SessionOptions {
	if len(o.HeaderNames) == 0 {
		o.HeaderNames = []string{defaultSessionHeader}
	}
	if o.DeviceHeader == "" {
		o.DeviceHeader = defaultDeviceHeader
	}
	if o.ClockSkew <= 0 {
		o.ClockSkew = 30 * time.Second
	}
	if !o.RequireExpires {
		o.RequireExpires = true
	}
	if !o.RequireDevice {
		o.RequireDevice = true
	}
	return o
}

func (o GatewayOptions) withDefaults() GatewayOptions {
	if o.UserIDHeader == "" {
		o.UserIDHeader = defaultGatewayUserHeader
	}
	if o.SessionHeader == "" {
		o.SessionHeader = defaultSessionHeader
	}
	if o.DeviceHeader == "" {
		o.DeviceHeader = defaultDeviceHeader
	}
	if o.IssuerHeader == "" {
		o.IssuerHeader = defaultGatewayIssuerHeader
	}
	if o.AudienceHeader == "" {
		o.AudienceHeader = defaultGatewayAudience
	}
	if o.ExpireHeader == "" {
		o.ExpireHeader = defaultGatewayExpireHeader
	}
	if o.SignatureHeader == "" {
		o.SignatureHeader = defaultGatewaySignHeader
	}
	if o.TimestampHeader == "" {
		o.TimestampHeader = defaultGatewayTimeHeader
	}
	if o.NonceHeader == "" {
		o.NonceHeader = defaultGatewayNonceHeader
	}
	if o.ClockSkew <= 0 {
		o.ClockSkew = 30 * time.Second
	}
	if !o.RequireExpires {
		o.RequireExpires = true
	}
	if !o.RequireDevice {
		o.RequireDevice = true
	}
	return o
}

func extractJWTToken(tr transport.Transporter, cfg JWTOptions) (string, bool) {
	headerValue := strings.TrimSpace(tr.RequestHeader().Get(cfg.Header))
	if headerValue != "" {
		token, err := stripBearerPrefix(headerValue, cfg.Prefix)
		if err != nil {
			return "", true
		}
		return token, true
	}

	req, ok := transportHTTPRequest(tr)
	if !ok {
		return "", false
	}
	for _, name := range cfg.CookieNames {
		cookie, err := req.Cookie(name)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		return strings.TrimSpace(cookie.Value), true
	}

	return "", false
}

func extractSessionID(tr transport.Transporter, cfg SessionOptions) (string, bool) {
	for _, headerName := range cfg.HeaderNames {
		value := strings.TrimSpace(tr.RequestHeader().Get(headerName))
		if value != "" {
			return value, true
		}
	}

	req, ok := transportHTTPRequest(tr)
	if !ok {
		return "", false
	}
	for _, cookieName := range cfg.CookieNames {
		cookie, err := req.Cookie(cookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		return strings.TrimSpace(cookie.Value), true
	}
	return "", false
}

func transportHTTPRequest(tr transport.Transporter) (*http.Request, bool) {
	httpTransporter, ok := tr.(kratoshttp.Transporter)
	if !ok || httpTransporter.Request() == nil {
		return nil, false
	}
	return httpTransporter.Request(), true
}

func stripBearerPrefix(value string, prefix string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
		return "", fmt.Errorf("authorization header must use %s", strings.TrimSpace(prefix))
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" {
		return "", fmt.Errorf("authorization token is empty")
	}
	return token, nil
}

func extractUserIDClaim(claims jwt.MapClaims, claimKeys []string) (string, error) {
	for _, key := range claimKeys {
		if value := readStringClaim(claims, key); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("jwt user id claim is missing")
}

func extractRequiredStringClaim(claims jwt.MapClaims, key string) (string, error) {
	value := readStringClaim(claims, key)
	if value == "" {
		return "", fmt.Errorf("jwt claim %s is missing", key)
	}
	return value, nil
}

func readStringClaim(claims jwt.MapClaims, key string) string {
	raw, ok := claims[key]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

type jsonNumber interface {
	String() string
}

func readTimeClaim(raw any) time.Time {
	switch v := raw.(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	case int:
		return time.Unix(int64(v), 0)
	case jsonNumber:
		if sec, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return time.Unix(sec, 0)
		}
	case string:
		return parseHeaderTime(v)
	}
	return time.Time{}
}

func normalizeAudienceClaim(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return trimSlice(v)
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				result = append(result, text)
			}
		}
		return result
	case string:
		return splitAndTrim(v, ",")
	default:
		return nil
	}
}

func validateDeviceHeader(header transport.Header, headerName string, expected string) error {
	actual := strings.TrimSpace(header.Get(headerName))
	if actual == "" {
		return fmt.Errorf("request device header %s is empty", headerName)
	}
	if actual != expected {
		return fmt.Errorf("request device header %s does not match token claim", headerName)
	}
	return nil
}

func validateOptionalDeviceHeader(header transport.Header, headerName string, expected string) error {
	if expected == "" {
		return nil
	}
	return validateDeviceHeader(header, headerName, expected)
}

func validateIssuer(actual string, expected string) error {
	if expected == "" {
		return nil
	}
	if strings.TrimSpace(actual) != expected {
		return fmt.Errorf("issuer does not match")
	}
	return nil
}

func validateAudience(actual []string, expected []string) error {
	if len(expected) == 0 {
		return nil
	}
	actualSet := make(map[string]struct{}, len(actual))
	for _, item := range actual {
		if item == "" {
			continue
		}
		actualSet[item] = struct{}{}
	}
	for _, item := range expected {
		if _, ok := actualSet[item]; ok {
			return nil
		}
	}
	return fmt.Errorf("audience does not match")
}

func validateExpiration(expireAt time.Time, required bool, clockSkew time.Duration, now time.Time) error {
	if expireAt.IsZero() {
		if required {
			return fmt.Errorf("expiration time is missing")
		}
		return nil
	}
	if !expireAt.After(now.Add(-clockSkew)) {
		return fmt.Errorf("authentication credential is expired")
	}
	return nil
}

func buildGatewayCanonicalString(header transport.Header, info AuthInfo, cfg GatewayOptions) string {
	values := []string{
		"user_id=" + info.UserID,
		"session_id=" + info.SessionID,
		"device_id=" + info.DeviceID,
		"issuer=" + info.Issuer,
		"audience=" + strings.Join(info.Audience, ","),
		"expires_at=" + header.Get(cfg.ExpireHeader),
		"timestamp=" + header.Get(cfg.TimestampHeader),
		"nonce=" + header.Get(cfg.NonceHeader),
	}
	return strings.Join(values, "\n")
}

func verifyGatewayHMACSignature(canonical string, signature string, secret []byte) error {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	expected := mac.Sum(nil)

	signature = strings.TrimSpace(signature)
	switch {
	case isHexString(signature):
		actual, err := hex.DecodeString(signature)
		if err != nil {
			return fmt.Errorf("gateway signature is not valid hex")
		}
		if !hmac.Equal(actual, expected) {
			return fmt.Errorf("gateway signature does not match")
		}
		return nil
	default:
		actual, err := base64.StdEncoding.DecodeString(signature)
		if err != nil {
			return fmt.Errorf("gateway signature must be hex or base64")
		}
		if !hmac.Equal(actual, expected) {
			return fmt.Errorf("gateway signature does not match")
		}
		return nil
	}
}

func parseHeaderTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if unixSecond, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unixSecond, 0)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Time{}
}

func trimSlice(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func splitAndTrim(value string, sep string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return trimSlice(strings.Split(value, sep))
}

func containsString(items []string, target string) bool {
	return slices.Contains(items, target)
}

func isHexString(value string) bool {
	if len(value)%2 != 0 || value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func errOrLast(current error, last error) error {
	if current != nil {
		return current
	}
	return last
}

func unauthorizedError(message string) error {
	return kerrors.Unauthorized("UNAUTHORIZED", message)
}
