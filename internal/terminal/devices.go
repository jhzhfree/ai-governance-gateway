package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/spf13/cobra"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func exclusivePrivate(path string, data []byte) error {
	if err := privateDirectory(filepath.Dir(path), true); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("拒绝覆盖已有密钥或无法创建私有文件")
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(path)
		return errors.New("私有文件写入失败")
	}
	if err := f.Sync(); err != nil {
		os.Remove(path)
		return errors.New("私有文件同步失败")
	}
	return nil
}
func generateDeviceFiles(keyPath, tokenPath string) (string, error) {
	if keyPath == "" || tokenPath == "" || filepath.Clean(keyPath) == filepath.Clean(tokenPath) {
		return "", input("必须指定不同的设备私钥和本机访问令牌文件")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := exclusivePrivate(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})); err != nil {
		return "", err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		os.Remove(keyPath)
		return "", err
	}
	if err := exclusivePrivate(tokenPath, []byte(base64.RawURLEncoding.EncodeToString(token)+"\n")); err != nil {
		os.Remove(keyPath)
		return "", err
	}
	return deviceproof.Public(pub), nil
}
func readDeviceKey(path string) (ed25519.PrivateKey, error) {
	data, err := readPrivate(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, input("设备私钥文件格式无效")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, input("设备私钥文件无效")
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, input("仅支持 Ed25519 设备私钥")
	}
	return ed, nil
}
func (c *Client) devicePost(ctx context.Context, path string, body any, proof string, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.server+path, bytes.NewReader(raw))
	if err != nil {
		return input("设备请求地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if proof != "" {
		req.Header.Set("DPoP", proof)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("设备服务连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, RequestID: requestID(resp.Header.Get("X-Request-ID"))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target); err != nil {
		return errors.New("设备服务响应无效")
	}
	return nil
}
func (c *Client) issueDeviceSession(ctx context.Context, org, project, device string, key ed25519.PrivateKey, models []string) (v types.IssuedDeviceSession, err error) {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/devices/" + url.PathEscape(device) + "/sessions"
	proof, err := deviceproof.Sign(key, "POST", c.server+path, "")
	if err != nil {
		return v, err
	}
	err = c.devicePost(ctx, path, map[string]any{"models": models}, proof, &v)
	return
}

type deviceProxy struct {
	permits                                chan struct{}
	client                                 *Client
	org, project, device, localToken, host string
	key                                    ed25519.PrivateKey
	models                                 []string
	mu                                     sync.Mutex
	session                                types.IssuedDeviceSession
	upstream                               *http.Client
}

func (p *deviceProxy) token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Until(p.session.ExpiresAt) > 15*time.Second {
		return p.session.Token, nil
	}
	v, err := p.client.issueDeviceSession(ctx, p.org, p.project, p.device, p.key, p.models)
	if err != nil {
		return "", err
	}
	if v.Token == "" || v.TokenType != "DPoP" || !v.ExpiresAt.After(time.Now()) {
		return "", errors.New("设备会话无效")
	}
	p.session = v
	return v.Token, nil
}
func (p *deviceProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Host != p.host || r.Header.Get("Origin") != "" {
		http.Error(w, "local_client_required", 403)
		return
	}
	token, err := identity.Bearer(r.Header.Get("Authorization"))
	if err != nil || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(token), []byte(p.localToken)) != 1 {
		http.Error(w, "local_authentication_required", 401)
		return
	}
	if r.URL.RawQuery != "" || !((r.Method == "POST" && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/embeddings")) || (r.Method == "GET" && r.URL.Path == "/v1/models")) {
		http.NotFound(w, r)
		return
	}
	select {
	case p.permits <- struct{}{}:
		defer func() { <-p.permits }()
	default:
		http.Error(w, "local_concurrency_exceeded", 429)
		return
	}
	limit := int64(64 << 10)
	if r.URL.Path == "/v1/chat/completions" {
		limit = 1 << 20
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "request_too_large", 413)
		return
	}
	session, err := p.token(r.Context())
	if err != nil {
		http.Error(w, "device_session_unavailable_login_or_restart_required", 503)
		return
	}
	endpoint := p.client.server + r.URL.EscapedPath()
	proof, err := deviceproof.Sign(p.key, r.Method, endpoint, session)
	if err != nil {
		http.Error(w, "device_proof_unavailable", 503)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid_request", 400)
		return
	}
	req.Header.Set("Authorization", "DPoP "+session)
	req.Header.Set("DPoP", proof)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.upstream.Do(req)
	if err != nil {
		http.Error(w, "gateway_unavailable", 502)
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{"Content-Type", "X-Request-ID", "WWW-Authenticate"} {
		if value := resp.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			_ = http.NewResponseController(w).Flush()
		}
		if readErr != nil {
			return
		}
	}
}
func runDeviceProxy(ctx context.Context, c *Client, org, project, device, keyPath, tokenPath, listen string, models []string, onListen func(string)) error {
	host, _, err := net.SplitHostPort(listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return input("本机代理只允许监听明确的回环 IP 地址")
	}
	if org == "" || project == "" || device == "" {
		return input("必须指定组织、项目和设备 ID")
	}
	key, err := readDeviceKey(keyPath)
	if err != nil {
		return err
	}
	raw, err := readPrivate(tokenPath)
	if err != nil {
		return err
	}
	localToken := strings.TrimSpace(string(raw))
	if len(localToken) < 32 || len(localToken) > 256 || strings.ContainsAny(localToken, "\r\n ") {
		return input("本机访问令牌文件无效")
	}
	if len(models) == 0 {
		var details types.ProjectDetails
		if err := c.get(ctx, "/api/v1/organizations/"+url.PathEscape(org)+"/projects/"+url.PathEscape(project), nil, &details); err != nil {
			return err
		}
		models = details.Models
	}
	if len(models) == 0 {
		return input("项目尚未配置可调用模型")
	}
	upstream := *c.http
	upstream.Timeout = 0
	if transport, ok := upstream.Transport.(*http.Transport); ok {
		copy := transport.Clone()
		copy.ResponseHeaderTimeout = 30 * time.Second
		upstream.Transport = copy
	} else if upstream.Transport == nil {
		copy := http.DefaultTransport.(*http.Transport).Clone()
		copy.ResponseHeaderTimeout = 30 * time.Second
		upstream.Transport = copy
	}
	upstream.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	proxy := &deviceProxy{permits: make(chan struct{}, 16), client: c, org: org, project: project, device: device, localToken: localToken, key: key, models: models, upstream: &upstream}
	if _, err := proxy.token(ctx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return errors.New("无法启动本机代理监听")
	}
	defer listener.Close()
	proxy.host = listener.Addr().String()
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if onListen != nil {
		onListen("http://" + proxy.host + "/v1")
	}
	select {
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			_ = server.Close()
		}
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("本机代理已停止")
	}
}
func (a *app) addDevices() {
	root := &cobra.Command{Use: "device", Short: "设备密钥、登记和本机 SDK 接入代理"}
	var keyFile, localFile string
	generate := &cobra.Command{Use: "keygen", Short: "生成私有 Ed25519 密钥与本机令牌文件（不覆盖已有文件）", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		public, err := generateDeviceFiles(keyFile, localFile)
		if err != nil {
			return err
		}
		return writeJSON(a.options.Out, map[string]string{"public_key": public, "private_key_file": keyFile, "local_token_file": localFile})
	}}
	generate.Flags().StringVar(&keyFile, "private-key-file", "", "私有目录中的设备私钥文件")
	generate.Flags().StringVar(&localFile, "local-token-file", "", "私有目录中的本机调用令牌文件")
	var org, name, registrationKey string
	register := &cobra.Command{Use: "register", Short: "使用当前登录身份登记公钥，等待组织管理员确认", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, _ := scope(cfg, org, "")
		if o == "" || name == "" {
			return input("必须指定组织和设备名称")
		}
		key, err := readDeviceKey(registrationKey)
		if err != nil {
			return err
		}
		var v types.Device
		if err := c.devicePost(cmd.Context(), "/api/v1/organizations/"+url.PathEscape(o)+"/devices", map[string]string{"name": name, "public_key": deviceproof.Public(key.Public().(ed25519.PublicKey))}, "", &v); err != nil {
			return err
		}
		return writeJSON(a.options.Out, v)
	}}
	register.Flags().StringVar(&org, "org", "", "组织 ID")
	register.Flags().StringVar(&name, "name", "", "设备名称")
	register.Flags().StringVar(&registrationKey, "private-key-file", "", "设备私钥文件（仅发送公钥）")
	var proxyOrg, project, device, proxyKey, proxyToken, listen string
	var models []string
	proxy := &cobra.Command{Use: "proxy", Short: "为本机 SDK 附加设备绑定会话和 DPoP 请求证明", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, c, err := a.client()
		if err != nil {
			return err
		}
		o, p := scope(cfg, proxyOrg, project)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDeviceProxy(ctx, c, o, p, device, proxyKey, proxyToken, listen, models, func(endpoint string) {
			fmt.Fprintln(a.options.Err, "本机 SDK base_url:", endpoint, "；本机 api_key 从受保护令牌文件读取。")
		})
	}}
	proxy.Flags().StringVar(&proxyOrg, "org", "", "组织 ID")
	proxy.Flags().StringVar(&project, "project", "", "项目 ID")
	proxy.Flags().StringVar(&device, "device", "", "已确认设备 ID")
	proxy.Flags().StringVar(&proxyKey, "private-key-file", "", "设备私钥文件")
	proxy.Flags().StringVar(&proxyToken, "local-token-file", "", "本机调用令牌文件")
	proxy.Flags().StringVar(&listen, "listen", "127.0.0.1:16680", "仅回环 IP 的监听地址")
	proxy.Flags().StringSliceVar(&models, "models", nil, "会话模型范围，默认项目已授权模型")
	root.AddCommand(generate, register, proxy)
	a.root.AddCommand(root)
}
