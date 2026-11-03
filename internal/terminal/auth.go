package terminal

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"golang.org/x/oauth2"
)

var ErrLoginDenied = errors.New("终端登录未完成或已被拒绝，请重新发起登录")

type Challenge struct {
	VerificationURI, UserCode string
	ExpiresAt                 time.Time
}

func transportURL(value string, allowHTTP bool) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Fragment != "" {
		return false
	}
	copy := *u
	copy.Path = ""
	copy.RawQuery = ""
	_, err = canonicalURL(copy.String(), allowHTTP)
	return err == nil
}

// DeviceLogin is for public native OIDC clients. Refresh tokens are not retained.
func DeviceLogin(ctx context.Context, c Config, httpClient *http.Client, onChallenge func(Challenge) error) (Credential, error) {
	var empty Credential
	if c.ClientID == "" || c.Audience == "" {
		return empty, errors.New("终端登录需要 client-id 和管理 API audience")
	}
	if _, err := canonicalURL(c.Server, c.AllowLoopbackHTTP); err != nil {
		return empty, err
	}
	if _, err := issuerURL(c.Issuer, c.AllowLoopbackHTTP); err != nil {
		return empty, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		*client = *httpClient
	}
	if client.Timeout == 0 {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, errors.New("无法读取身份服务配置")
	}
	var metadata struct {
		DeviceEndpoint string `json:"device_authorization_endpoint"`
		JWKS           string `json:"jwks_uri"`
	}
	if provider.Claims(&metadata) != nil || metadata.DeviceEndpoint == "" {
		return empty, errors.New("身份服务未提供 Device Flow；请配置支持该流程的公共终端客户端")
	}
	endpoint := provider.Endpoint()
	endpoint.DeviceAuthURL = metadata.DeviceEndpoint
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	if !transportURL(endpoint.DeviceAuthURL, c.AllowLoopbackHTTP) || !transportURL(endpoint.TokenURL, c.AllowLoopbackHTTP) || !transportURL(metadata.JWKS, c.AllowLoopbackHTTP) {
		return empty, errors.New("身份服务返回了不安全的授权端点")
	}
	oauth := oauth2.Config{ClientID: c.ClientID, Scopes: []string{"openid", "aigw:manage"}, Endpoint: endpoint}
	challenge, err := oauth.DeviceAuth(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, ErrLoginDenied
	}
	if challenge.DeviceCode == "" || len(challenge.DeviceCode) > 4096 || challenge.UserCode == "" || len(challenge.UserCode) > 128 || SafeText(challenge.UserCode) != challenge.UserCode || challenge.Interval < 0 || challenge.Interval > 300 || !challenge.Expiry.After(time.Now()) || challenge.Expiry.After(time.Now().Add(time.Hour)) || !transportURL(challenge.VerificationURI, c.AllowLoopbackHTTP) {
		return empty, errors.New("身份服务返回的设备授权信息无效")
	}
	if onChallenge != nil {
		if err := onChallenge(Challenge{challenge.VerificationURI, challenge.UserCode, challenge.Expiry}); err != nil {
			return empty, errors.New("无法展示设备授权信息")
		}
	}
	token, err := oauth.DeviceAccessToken(ctx, challenge)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, ErrLoginDenied
	}
	verifier, err := identity.NewOIDC(ctx, c.Issuer, c.Audience)
	if err != nil {
		return empty, errors.New("无法验证管理 API 令牌")
	}
	user, expiry, err := verifier.Verify(ctx, token.AccessToken)
	if err != nil {
		return empty, ErrLoginDenied
	}
	if !token.Expiry.IsZero() && token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	if !expiry.After(time.Now()) {
		return empty, ErrLoginDenied
	}
	api, err := NewClient(c.Server, token.AccessToken, c.AllowLoopbackHTTP, client)
	if err != nil {
		return empty, err
	}
	if _, err := api.Organizations(ctx, "", 1); err != nil {
		return empty, err
	}
	return Credential{Server: c.Server, Issuer: c.Issuer, ClientID: c.ClientID, Audience: c.Audience, AccessToken: token.AccessToken, ExpiresAt: expiry, User: user}, nil
}
