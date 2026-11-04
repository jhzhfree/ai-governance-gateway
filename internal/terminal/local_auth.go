package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

// LocalLogin only sends a password to the explicitly selected, canonical server.
func LocalLogin(ctx context.Context, c Config, username, password string, transport *http.Client) (Credential, error) {
	var empty Credential
	if _, err := canonicalURL(c.Server, c.AllowLoopbackHTTP); err != nil {
		return empty, err
	}
	if c.Issuer != c.Server+"/auth/local" || c.ClientID != "aigw-local" || c.Audience != "aigw-management" {
		return empty, errors.New("本地登录绑定无效")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if transport != nil {
		*client = *transport
	}
	if client.Timeout == 0 {
		client.Timeout = 10 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, "GET", c.Server+"/auth/config", nil)
	if err != nil {
		return empty, err
	}
	response, err := client.Do(request)
	if err != nil {
		return empty, errors.New("无法读取本地登录方式")
	}
	var config struct {
		Mode string `json:"mode"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&config)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || config.Mode != "local" {
		return empty, errors.New("目标网关未启用本地登录")
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	request, err = http.NewRequestWithContext(ctx, "POST", c.Server+"/auth/local/token", bytes.NewReader(payload))
	if err != nil {
		return empty, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.Server)
	response, err = client.Do(request)
	if err != nil {
		return empty, errors.New("本地登录请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode == 403 {
		return empty, errors.New("请先在 Web 完成首次或重置后的密码变更")
	}
	if response.StatusCode != 200 {
		return empty, &APIError{Status: response.StatusCode, RequestID: requestID(response.Header.Get("X-Request-ID"))}
	}
	var token struct {
		AccessToken string             `json:"access_token"`
		ExpiresAt   time.Time          `json:"expires_at"`
		User        identity.Principal `json:"user"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&token) != nil || !strings.HasPrefix(token.AccessToken, "aigw_local_") || len(token.AccessToken) > 256 || strings.IndexFunc(token.AccessToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || token.User.Issuer != c.Issuer || token.User.Subject == "" || !token.ExpiresAt.After(time.Now()) || token.ExpiresAt.After(time.Now().Add(2*time.Hour+time.Minute)) {
		return empty, ErrLoginDenied
	}
	api, err := NewClient(c.Server, token.AccessToken, c.AllowLoopbackHTTP, client)
	if err != nil {
		return empty, err
	}
	if _, err := api.Organizations(ctx, "", 1); err != nil {
		return empty, err
	}
	return Credential{Server: c.Server, Issuer: c.Issuer, ClientID: c.ClientID, Audience: c.Audience, AccessToken: token.AccessToken, ExpiresAt: token.ExpiresAt, User: token.User}, nil
}

func RevokeLocalCredential(ctx context.Context, c Config, secret string, transport *http.Client) error {
	if _, err := canonicalURL(c.Server, c.AllowLoopbackHTTP); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if transport != nil {
		*client = *transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, "POST", c.Server+"/auth/local/logout", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Origin", c.Server)
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 204 && response.StatusCode != 401 {
		return errors.New("无法确认服务端令牌撤销")
	}
	return nil
}
