// Package terminal implements the CLI/TUI client without direct database access.
package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/zalando/go-keyring"
)

var ErrNotLoggedIn = errors.New("尚未登录，请运行 aigw login，或为自动化任务设置 AIGW_TOKEN")

type Config struct {
	Version           int    `json:"version"`
	Server            string `json:"server"`
	Issuer            string `json:"issuer,omitempty"`
	ClientID          string `json:"client_id,omitempty"`
	Audience          string `json:"audience,omitempty"`
	Organization      string `json:"organization,omitempty"`
	Project           string `json:"project,omitempty"`
	CredentialStore   string `json:"credential_store,omitempty"`
	AllowLoopbackHTTP bool   `json:"allow_loopback_http,omitempty"`
}
type Credential struct {
	Server      string             `json:"server"`
	Issuer      string             `json:"issuer"`
	ClientID    string             `json:"client_id"`
	Audience    string             `json:"audience"`
	AccessToken string             `json:"access_token"`
	ExpiresAt   time.Time          `json:"expires_at"`
	User        identity.Principal `json:"user"`
}

func (Credential) String() string   { return "[redacted credential]" }
func (Credential) GoString() string { return "[redacted credential]" }

func (c Config) sameBinding(other Config) bool {
	return c.Server == other.Server && c.Issuer == other.Issuer && c.ClientID == other.ClientID && c.Audience == other.Audience
}
func (c Credential) matches(config Config) bool {
	return c.Server == config.Server && c.Issuer == config.Issuer && c.ClientID == config.ClientID && c.Audience == config.Audience
}

type Keyring interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
	Delete(string, string) error
}
type SystemKeyring struct{}

func (SystemKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (SystemKeyring) Set(s, u, v string) error        { return keyring.Set(s, u, v) }
func (SystemKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

type State struct {
	Path    string
	Keyring Keyring
}

func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("无法确定用户配置目录，请使用 --config")
	}
	return filepath.Join(dir, "aigw", "config.json"), nil
}
func canonicalURL(value string, allowHTTP bool) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("服务或身份地址必须是不含凭证、查询参数和路径前缀的 origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP && net.ParseIP(u.Hostname()).IsLoopback()) {
		return "", errors.New("连接需要 HTTPS；本机 IP 的 HTTP 开发连接须显式启用 --allow-loopback-http")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	return u.String(), nil
}

// Issuers may use realm paths; only the API server is restricted to an origin.
func issuerURL(value string, allowHTTP bool) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("身份地址无效")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP && net.ParseIP(u.Hostname()).IsLoopback()) {
		return "", errors.New("身份地址需要 HTTPS 或显式允许的本机 HTTP")
	}
	return value, nil
}
func privateDirectory(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return errors.New("无法创建私有配置目录")
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !create {
		return nil
	}
	if err != nil {
		return errors.New("无法读取配置目录")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("配置目录必须是权限为 0700 的真实目录，不能使用共享目录或符号链接")
	}
	return nil
}
func readPrivate(path string) ([]byte, error) {
	if err := privateDirectory(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, errors.New("无法读取本地文件")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return nil, errors.New("本地配置/凭证必须是权限为 0600 的普通文件，不能使用符号链接")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("无法读取本地文件")
	}
	return data, nil
}
func writePrivate(path string, data []byte) error {
	if err := privateDirectory(filepath.Dir(path), true); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("拒绝覆盖不安全的配置/凭证文件")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".aigw-*")
	if err != nil {
		return errors.New("无法写入本地文件")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return errors.New("本地文件写入失败")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return errors.New("本地文件同步失败")
	}
	if err := file.Close(); err != nil {
		return errors.New("本地文件关闭失败")
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("本地文件替换失败")
	}
	return nil
}
func (s State) Load() (Config, bool, error) {
	data, err := readPrivate(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if json.Unmarshal(data, &c) != nil || c.Version != 1 {
		return Config{}, false, errors.New("配置文件无效或版本不受支持")
	}
	normalized, err := canonicalURL(c.Server, c.AllowLoopbackHTTP)
	if err != nil || normalized != c.Server {
		return Config{}, false, errors.New("配置中的服务地址无效")
	}
	return c, true, nil
}
func (s State) Save(c Config) error {
	if normalized, err := canonicalURL(c.Server, c.AllowLoopbackHTTP); err != nil || normalized != c.Server {
		return errors.New("拒绝保存无效的服务配置")
	}
	c.Version = 1
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return errors.New("无法编码配置")
	}
	return writePrivate(s.Path, append(data, '\n'))
}
func (s State) credentialID(c Config) string {
	h := sha256.Sum256([]byte(s.Path + "\x00" + c.Server + "\x00" + c.Issuer + "\x00" + c.ClientID + "\x00" + c.Audience))
	return hex.EncodeToString(h[:])
}
func (s State) backend() Keyring {
	if s.Keyring != nil {
		return s.Keyring
	}
	return SystemKeyring{}
}
func (s State) SaveCredential(c Config, cred Credential) error {
	if !cred.matches(c) || cred.AccessToken == "" {
		return errors.New("凭证与登录配置不匹配")
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return errors.New("无法编码凭证")
	}
	switch c.CredentialStore {
	case "file":
		return writePrivate(s.Path+".credentials.json", data)
	case "keyring":
		if err := s.backend().Set("ai-governance-gateway", s.credentialID(c), string(data)); err != nil {
			return errors.New("系统钥匙串不可用；如确有需要，请显式使用 --credential-store=file 重新登录")
		}
		return nil
	default:
		return errors.New("凭证存储方式无效")
	}
}
func (s State) Credential(c Config) (Credential, error) {
	var data []byte
	var err error
	switch c.CredentialStore {
	case "file":
		data, err = readPrivate(s.Path + ".credentials.json")
	case "keyring":
		var value string
		value, err = s.backend().Get("ai-governance-gateway", s.credentialID(c))
		data = []byte(value)
	default:
		return Credential{}, ErrNotLoggedIn
	}
	if err != nil {
		return Credential{}, ErrNotLoggedIn
	}
	var cred Credential
	if len(data) > 64<<10 || json.Unmarshal(data, &cred) != nil || !cred.matches(c) {
		return cred, errors.New("本地凭证与服务配置不匹配，请重新登录")
	}
	if !cred.ExpiresAt.After(time.Now()) {
		return Credential{}, errors.New("终端登录已过期，请重新运行 aigw login")
	}
	if cred.AccessToken == "" {
		return Credential{}, ErrNotLoggedIn
	}
	return cred, nil
}
func (s State) DeleteCredential(c Config) error {
	switch c.CredentialStore {
	case "file":
		if _, err := readPrivate(s.Path + ".credentials.json"); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := os.Remove(s.Path + ".credentials.json"); err != nil {
			return errors.New("无法删除本地凭证")
		}
	case "keyring":
		if err := s.backend().Delete("ai-governance-gateway", s.credentialID(c)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return errors.New("无法从系统钥匙串删除凭证")
		}
	case "":
		return nil
	default:
		return errors.New("凭证存储方式无效")
	}
	return nil
}
func writeJSON(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }
