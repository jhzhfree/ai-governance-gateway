package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
)

type APIError struct {
	Status    int
	RequestID string
}

var requestIDPattern = regexp.MustCompile(`^(?:[A-Z2-7]{26}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

func requestID(value string) string {
	if requestIDPattern.MatchString(value) {
		return value
	}
	return ""
}

func (e *APIError) Error() string {
	message := "服务暂时不可用，请稍后重试"
	switch e.Status {
	case 401:
		message = "登录无效或已过期，请重新登录"
	case 403:
		message = "当前身份没有访问权限"
	case 404:
		message = "资源不存在或无权访问"
	case 429:
		message = "请求过于频繁，请稍后重试"
	}
	if e.RequestID != "" {
		message += "（请求编号：" + SafeText(e.RequestID) + "）"
	}
	return message
}
func SafeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
}

type Client struct {
	server, token string
	http          *http.Client
}

func NewClient(server, token string, allowHTTP bool, transport *http.Client) (*Client, error) {
	normalized, err := canonicalURL(server, allowHTTP)
	if err != nil {
		return nil, err
	}
	if token == "" || len(token) > 16<<10 || strings.ContainsAny(token, "\r\n") {
		return nil, ErrNotLoggedIn
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if transport != nil {
		*client = *transport
	}
	if client.Timeout == 0 {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{server: normalized, token: token, http: client}, nil
}
func (c *Client) get(ctx context.Context, path string, query url.Values, target any) error {
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "?") {
		return errors.New("客户端请求路径无效")
	}
	endpoint := c.server + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("无法构造 API 请求")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("无法连接服务，请检查地址与网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &APIError{Status: resp.StatusCode, RequestID: requestID(resp.Header.Get("X-Request-ID"))}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("服务响应不是 JSON API")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("服务响应读取失败")
	}
	if len(data) > 1<<20 {
		return errors.New("服务响应超过客户端限制")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return errors.New("服务响应与 API 格式不符")
	}
	required := []string{"data", "next_cursor"}
	switch target.(type) {
	case *types.RequestRecord:
		required = []string{"request_id", "period", "currency", "price_version", "state", "reserved", "actual", "created_at", "updated_at"}
	case *types.Usage:
		required = []string{"period", "currency", "limit_amount", "spent", "held"}
	case *types.ProjectDetails:
		required = []string{"id", "organization_id", "name", "role", "models"}
	}
	for _, name := range required {
		if value, ok := object[name]; !ok || (string(value) == "null" && name != "actual") {
			return errors.New("服务响应缺少必需字段")
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("服务响应与 API 格式不符")
	}
	return nil
}
func pagination(after string, limit int) (url.Values, error) {
	if len(after) > 128 || limit < 1 || limit > 100 {
		return nil, input("分页参数无效，limit 必须为 1–100")
	}
	return url.Values{"after": {after}, "limit": {strconv.Itoa(limit)}}, nil
}
func projectPath(org, project string) (string, error) {
	if org == "" || project == "" {
		return "", input("请先用 context use 选择组织和项目")
	}
	return "/api/v1/organizations/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project), nil
}
func (c *Client) Organizations(ctx context.Context, after string, limit int) (v types.OrganizationPage, err error) {
	q, err := pagination(after, limit)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, "/api/v1/organizations", q, &v)
	return
}
func (c *Client) Projects(ctx context.Context, org, after string, limit int) (v types.ProjectPage, err error) {
	if org == "" {
		return v, input("请先选择组织，或传入 --org")
	}
	q, err := pagination(after, limit)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, "/api/v1/organizations/"+url.PathEscape(org)+"/projects", q, &v)
	return
}
func (c *Client) Project(ctx context.Context, org, project string) (v types.ProjectDetails, err error) {
	p, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, p, nil, &v)
	return
}
func (c *Client) Keys(ctx context.Context, org, project, after string, limit int) (v types.KeyPage, err error) {
	p, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	q, err := pagination(after, limit)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, p+"/keys", q, &v)
	return
}
func (c *Client) Usage(ctx context.Context, org, project string) (v types.Usage, err error) {
	p, err := projectPath(org, project)
	if err != nil {
		return v, err
	}
	err = c.get(ctx, p+"/usage", nil, &v)
	return
}
func (c *Client) Audit(ctx context.Context, org, after string, limit int) (v types.AuditPage, err error) {
	if org == "" {
		return v, input("请先选择组织")
	}
	q, err := pagination(after, limit)
	if err != nil {
		return v, err
	}
	if after != "" {
		n, e := strconv.ParseInt(after, 10, 64)
		if e != nil || n < 0 {
			return v, input("审计游标必须是非负整数")
		}
	}
	err = c.get(ctx, "/api/v1/organizations/"+url.PathEscape(org)+"/audit", q, &v)
	return
}
func (c *Client) String() string {
	return fmt.Sprintf("API client for %s (credential redacted)", c.server)
}
