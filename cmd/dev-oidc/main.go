// Command dev-oidc is a development-only OpenID Connect provider for offline
// loopback work. It auto-approves every login and issues tokens with the
// aigw:manage scope. It must never be exposed beyond 127.0.0.1 and is not part
// of any release package.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type config struct {
	listen    string
	certDir   string
	audience  string
	clientID  string
	subject   string
	scope     string
	printOnly bool
}

func main() {
	var c config
	flag.StringVar(&c.listen, "listen", "127.0.0.1:9443", "loopback listener for the development IdP")
	flag.StringVar(&c.certDir, "cert-dir", ".aigw/dev-oidc", "directory holding the local CA, TLS certificate and signing key")
	flag.StringVar(&c.audience, "audience", "aigw-dev", "audience of issued API tokens; must equal AIGW_OIDC_AUDIENCE")
	flag.StringVar(&c.clientID, "client-id", "aigw-dev-console", "OAuth client id accepted for browser login")
	flag.StringVar(&c.subject, "subject", "dev-auditor", "subject claim of issued tokens")
	flag.StringVar(&c.scope, "scope", "aigw:manage", "scope claim of issued API tokens")
	flag.BoolVar(&c.printOnly, "print-token", false, "print one API token for the given flags and exit")
	flag.Parse()

	host, _, err := net.SplitHostPort(c.listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("dev IdP only listens on a loopback IP address")
	}
	issuer := "https://" + c.listen
	material, err := loadOrCreate(c.certDir)
	if err != nil {
		log.Fatal("dev IdP setup failed: ", err)
	}
	if c.printOnly {
		fmt.Println(sign(material.key, map[string]any{
			"iss": issuer, "sub": c.subject, "aud": c.audience,
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "scope": c.scope,
		}))
		return
	}

	idp := &provider{cfg: c, issuer: issuer, material: material, codes: map[string]authorization{}, devices: map[string]*deviceGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", idp.discovery)
	mux.HandleFunc("GET /jwks", idp.jwks)
	mux.HandleFunc("GET /authorize", idp.authorize)
	mux.HandleFunc("POST /token", idp.token)
	mux.HandleFunc("POST /device/authorize", idp.deviceAuthorize)
	mux.HandleFunc("POST /device/verify", idp.deviceVerify)

	server := &http.Server{
		Addr:              c.listen,
		Handler:           mux,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{material.certificate}},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	log.Printf("development IdP (NOT FOR PRODUCTION) issuer=%s subject=%s audience=%s", issuer, c.subject, c.audience)
	log.Printf("trust bundle: %s (export SSL_CERT_FILE=%s for the gateway)", material.caPath, material.caPath)
	log.Fatal(server.ListenAndServeTLS("", ""))
}

type material struct {
	key         *rsa.PrivateKey
	certificate tls.Certificate
	caPath      string
}

// loadOrCreate keeps the CA stable across restarts so a previously exported
// SSL_CERT_FILE keeps working; every file is private to the current user.
func loadOrCreate(dir string) (*material, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	caPath, certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key")
	if _, err := os.Stat(certPath); err != nil {
		if err := generate(dir, caPath, certPath, keyPath); err != nil {
			return nil, err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	key, err := loadKey(keyPath)
	if err != nil {
		return nil, err
	}
	return &material{key: key, certificate: cert, caPath: caPath}, nil
}

func generate(dir, caPath, certPath, keyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aigw dev IdP CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "aigw dev IdP"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writePEM(caPath, "CERTIFICATE", caDER); err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", leafDER); err != nil {
		return err
	}
	return writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
}

func writePEM(path, kind string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600)
}

func loadKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid signing key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

type authorization struct {
	client, redirect, nonce, challenge string
}

type deviceGrant struct {
	client, userCode string
	approved         bool
	polls            int
}

type provider struct {
	cfg      config
	issuer   string
	material *material
	mu       sync.Mutex
	codes    map[string]authorization
	devices  map[string]*deviceGrant
}

func (p *provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                p.issuer,
		"jwks_uri":                              p.issuer + "/jwks",
		"authorization_endpoint":                p.issuer + "/authorize",
		"token_endpoint":                        p.issuer + "/token",
		"device_authorization_endpoint":         p.issuer + "/device/authorize",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "aigw:manage"},
	})
}

func (p *provider) jwks(w http.ResponseWriter, _ *http.Request) {
	key := p.material.key
	writeJSON(w, map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "kid": "dev-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
}

func (p *provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
		http.Error(w, "development IdP requires PKCE S256 and a nonce", 400)
		return
	}
	if q.Get("client_id") != p.cfg.clientID {
		http.Error(w, "unknown client", 400)
		return
	}
	code := rand.Text()
	p.mu.Lock()
	p.codes[code] = authorization{q.Get("client_id"), q.Get("redirect_uri"), q.Get("nonce"), q.Get("code_challenge")}
	p.mu.Unlock()
	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect_uri", 400)
		return
	}
	v := target.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	target.RawQuery = v.Encode()
	log.Printf("auto-approved browser login for %s (development only)", p.cfg.subject)
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if r.Form.Get("grant_type") == "urn:ietf:params:oauth:grant-type:device_code" {
		p.deviceToken(w, r)
		return
	}
	p.mu.Lock()
	flow, ok := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	client := r.Form.Get("client_id")
	if basic, _, hasBasic := r.BasicAuth(); hasBasic {
		client = basic
	}
	if !ok || r.Form.Get("grant_type") != "authorization_code" || client != flow.client ||
		r.Form.Get("redirect_uri") != flow.redirect ||
		base64.RawURLEncoding.EncodeToString(hash[:]) != flow.challenge {
		http.Error(w, "invalid grant", 400)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": p.issue(p.cfg.audience, nil),
		"id_token":     p.issue(flow.client, map[string]any{"nonce": flow.nonce}),
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

func (p *provider) deviceAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != p.cfg.clientID {
		http.Error(w, "unknown device client", 400)
		return
	}
	code, userCode := rand.Text()+rand.Text(), rand.Text()[:8]
	p.mu.Lock()
	p.devices[code] = &deviceGrant{client: r.Form.Get("client_id"), userCode: userCode}
	p.mu.Unlock()
	writeJSON(w, map[string]any{
		"device_code": code, "user_code": userCode, "verification_uri": p.issuer + "/device/verify",
		"expires_in": 300, "interval": 1,
	})
}

func (p *provider) deviceVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid verification", 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, grant := range p.devices {
		if grant.userCode == r.Form.Get("user_code") {
			grant.approved = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.Error(w, "invalid code", 400)
}

// deviceToken approves the device flow on the first poll so the CLI can log in
// offline without a second browser.
func (p *provider) deviceToken(w http.ResponseWriter, r *http.Request) {
	code := r.Form.Get("device_code")
	p.mu.Lock()
	grant, ok := p.devices[code]
	if !ok || grant.client != r.Form.Get("client_id") {
		p.mu.Unlock()
		http.Error(w, "invalid device grant", 400)
		return
	}
	grant.approved = true
	delete(p.devices, code)
	p.mu.Unlock()
	writeJSON(w, map[string]any{
		"access_token": p.issue(p.cfg.audience, nil),
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

func (p *provider) issue(audience string, extra map[string]any) string {
	claims := map[string]any{
		"iss": p.issuer, "sub": p.cfg.subject, "aud": audience,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	if audience == p.cfg.audience {
		claims["scope"] = p.cfg.scope
	}
	for k, v := range extra {
		claims[k] = v
	}
	return sign(p.material.key, claims)
}

func sign(key *rsa.PrivateKey, claims map[string]any) string {
	body, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"dev-key","typ":"at+jwt"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
