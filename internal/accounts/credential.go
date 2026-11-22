package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autoclaw2api/internal/protocol"
)

// ErrMissingAccessToken 表示凭证 JSON 中不存在有效访问令牌。
var ErrMissingAccessToken = errors.New("凭证缺少有效的 accessToken")

// Credential 表示一份落盘的 AutoClaw 账号凭证。
type Credential struct {
	AccessToken     string          `json:"accessToken"`
	RefreshToken    string          `json:"refreshToken,omitempty"`
	ExpiresAt       int64           `json:"expiresAt,omitempty"`
	UserID          string          `json:"userId,omitempty"`
	UserName        string          `json:"userName,omitempty"`
	Alias           string          `json:"alias,omitempty"`
	Note            string          `json:"note,omitempty"`
	Disabled        bool            `json:"disabled,omitempty"`
	Phone           string          `json:"phone,omitempty"`
	DeviceID        string          `json:"deviceId,omitempty"`
	Region          protocol.Region `json:"region"`
	SandboxID       string          `json:"sandboxId,omitempty"`
	SandboxEndpoint string          `json:"sandboxEndpoint,omitempty"`
}

// ParseCredentialJSON 解析凭证 JSON 字节流，并依据文件名与字段内容推断所属区域。
func ParseCredentialJSON(raw []byte, fileName string) (*Credential, error) {
	if len(raw) == 0 {
		return nil, errors.New("凭证内容为空")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("凭证不是合法的 JSON: %w", err)
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				if s := protocol.StringFromAny(v); s != "" {
					return s
				}
			}
		}
		return ""
	}

	cred := &Credential{
		AccessToken:     getStr("accessToken", "access_token", "token"),
		RefreshToken:    getStr("refreshToken", "refresh_token"),
		UserID:          getStr("userId", "user_id", "uid"),
		UserName:        getStr("userName", "user_name", "name", "label"),
		Alias:           getStr("alias"),
		Note:            getStr("note", "remark"),
		Phone:           getStr("phone"),
		DeviceID:        getStr("deviceId", "device_id"),
		SandboxID:       getStr("sandboxId", "sandbox_id"),
		SandboxEndpoint: getStr("sandboxEndpoint", "sandbox_endpoint"),
	}
	if b, ok := m["disabled"].(bool); ok {
		cred.Disabled = b
	}
	if exp := parseUnixSeconds(m, "expiresAt", "expires_at"); exp > 0 {
		cred.ExpiresAt = exp
	} else if jwtExp := protocol.JWTExpirySeconds(cred.AccessToken); jwtExp > 0 {
		cred.ExpiresAt = jwtExp
	}
	if cred.UserID == "" {
		cred.UserID = protocol.JWTUserID(cred.AccessToken)
	}
	if cred.DeviceID == "" {
		cred.DeviceID = cred.UserID
	}
	cred.Region = detectRegion(m, fileName)

	if strings.TrimSpace(cred.AccessToken) == "" {
		return nil, ErrMissingAccessToken
	}
	return cred, nil
}

func detectRegion(m map[string]any, fileName string) protocol.Region {
	for _, k := range []string{"region", "realm"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return protocol.NormalizeRegion(v)
		}
	}
	if v, ok := m["vendor"].(string); ok {
		low := strings.ToLower(strings.TrimSpace(v))
		if strings.Contains(low, "global") || strings.Contains(low, "intl") {
			return protocol.RegionGlobal
		}
		if strings.Contains(low, "cn") {
			return protocol.RegionCN
		}
	}
	base := strings.ToLower(filepath.Base(fileName))
	if strings.HasPrefix(base, protocol.ChannelGlobal) || strings.Contains(base, "global") || strings.Contains(base, "intl") {
		return protocol.RegionGlobal
	}
	return protocol.RegionCN
}

func parseUnixSeconds(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case float64:
			n := int64(v)
			if n > 1e12 { // 兼容毫秒时间戳
				n /= 1000
			}
			if n > 0 {
				return n
			}
		case string:
			s := strings.TrimSpace(v)
			if s == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.Unix()
			}
			if f := protocol.FloatFromAny(s); f > 0 {
				n := int64(f)
				if n > 1e12 {
					n /= 1000
				}
				return n
			}
		}
	}
	return 0
}

// IsSupportedAuthFileName 判断文件名是否符合凭证扫描规则（兼容 autoclawcn-*.json 与 autoclawglobal-*.json）。
func IsSupportedAuthFileName(name string) bool {
	low := strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if !strings.HasSuffix(low, ".json") {
		return false
	}
	return strings.HasPrefix(low, protocol.ChannelCN+"-") ||
		strings.HasPrefix(low, protocol.ChannelGlobal+"-") ||
		strings.HasPrefix(low, "autoclaw-")
}

// BuildCredentialFileName 为新登录或导入的账号生成稳定的凭证文件名。
func BuildCredentialFileName(region protocol.Region, uid, accessToken string) string {
	prefix := region.Channel()
	id := sanitizeFileToken(uid)
	if id == "" {
		tokenTrim := strings.TrimSpace(accessToken)
		if len(tokenTrim) >= 8 {
			id = sanitizeFileToken(tokenTrim[len(tokenTrim)-8:])
		}
	}
	if id == "" {
		id = protocol.RandomHex(10)
	}
	return fmt.Sprintf("%s-%s.json", prefix, id)
}

func sanitizeFileToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	res := b.String()
	if len(res) > 36 {
		return res[:36]
	}
	return res
}

// DisplayLabel 返回用于控制台展示的账号名称（优先别名 → 昵称 → 脱敏手机号 → UID → 脱敏令牌）。
func (c *Credential) DisplayLabel() string {
	if s := strings.TrimSpace(c.Alias); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.UserName); s != "" {
		return s
	}
	if s := RedactPhoneNumber(c.Phone); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.UserID); s != "" {
		return s
	}
	return MaskToken(c.AccessToken)
}

// ToStorageJSON 序列化为规范化落盘 JSON。
func (c *Credential) ToStorageJSON() ([]byte, error) {
	payload := map[string]any{
		"vendor":       c.Region.Channel(),
		"region":       string(c.Region),
		"accessToken":  c.AccessToken,
		"refreshToken": c.RefreshToken,
		"expiresAt":    c.ExpiresAt,
	}
	if c.UserID != "" {
		payload["userId"] = c.UserID
	}
	if c.UserName != "" {
		payload["userName"] = c.UserName
	}
	if c.Alias != "" {
		payload["alias"] = c.Alias
	}
	if c.Note != "" {
		payload["note"] = c.Note
	}
	if c.Disabled {
		payload["disabled"] = true
	}
	if c.Phone != "" {
		payload["phone"] = c.Phone
	}
	if c.DeviceID != "" {
		payload["deviceId"] = c.DeviceID
	}
	if c.SandboxID != "" {
		payload["sandboxId"] = c.SandboxID
	}
	if c.SandboxEndpoint != "" {
		payload["sandboxEndpoint"] = c.SandboxEndpoint
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// MergeTokensLossless 无损合并写回：仅更新 accessToken、refreshToken 与 expiresAt，完整保留原始 JSON 中的所有自定义字段。
func MergeTokensLossless(original []byte, accessToken, refreshToken string, expiresAt int64) ([]byte, error) {
	target := make(map[string]any)
	if len(original) > 0 {
		if err := json.Unmarshal(original, &target); err != nil {
			return nil, fmt.Errorf("解析原凭证文件失败: %w", err)
		}
	}
	if strings.TrimSpace(accessToken) != "" {
		target["accessToken"] = strings.TrimSpace(accessToken)
		if _, hasSnake := target["access_token"]; hasSnake {
			target["access_token"] = strings.TrimSpace(accessToken)
		}
	}
	if strings.TrimSpace(refreshToken) != "" {
		target["refreshToken"] = strings.TrimSpace(refreshToken)
		if _, hasSnake := target["refresh_token"]; hasSnake {
			target["refresh_token"] = strings.TrimSpace(refreshToken)
		}
	}
	if expiresAt > 0 {
		target["expiresAt"] = expiresAt
		if _, hasSnake := target["expires_at"]; hasSnake {
			target["expires_at"] = expiresAt
		}
	}
	out, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// PatchFieldsLossless 无损更新凭证 JSON 中的指定元数据字段（如 alias、note、disabled），保留其余全部字段。
func PatchFieldsLossless(path string, patch map[string]any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	target := make(map[string]any)
	if err := json.Unmarshal(raw, &target); err != nil {
		return err
	}
	for k, v := range patch {
		if v == nil {
			delete(target, k)
		} else {
			target[k] = v
		}
	}
	out, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeFileAtomic(path, out)
}

func writeFileAtomic(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RedactPhoneNumber 对手机号执行中间四位脱敏。
func RedactPhoneNumber(phone string) string {
	r := []rune(strings.TrimSpace(phone))
	if len(r) < 7 {
		return strings.TrimSpace(phone)
	}
	return string(r[:3]) + "****" + string(r[len(r)-4:])
}

// MaskToken 对令牌字符串做首尾脱敏展示。
func MaskToken(token string) string {
	t := strings.TrimSpace(token)
	if len(t) <= 10 {
		return "***"
	}
	return t[:6] + "..." + t[len(t)-4:]
}
