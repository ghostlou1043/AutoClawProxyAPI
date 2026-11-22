package proxypool

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParsedEntry 表示从文本/订阅中提取的一条待导入记录。
type ParsedEntry struct {
	Line        int
	Name        string
	Protocol    string
	Normalized  string
	Unsupported bool
	Reason      string
}

var supportedSchemes = map[string]string{
	"http":    "80",
	"https":   "443",
	"socks5":  "1080",
	"socks5h": "1080",
}

var unsupportedSchemes = map[string]bool{
	"ss": true, "ssr": true, "vmess": true, "vless": true, "trojan": true,
	"hysteria": true, "hysteria2": true, "hy2": true, "tuic": true,
	"wireguard": true, "wg": true, "snell": true, "socks4": true,
	"socks4a": true, "anytls": true, "ssh": true,
}

// NormalizeProxyURL 校验并归一化代理地址（仅允许 http / https / socks5 / socks5h）。
// 返回规范化 URL、协议名、是否包含密码以及错误。
func NormalizeProxyURL(raw string) (normalized string, scheme string, hasPassword bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false, fmt.Errorf("代理地址为空")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", false, fmt.Errorf("代理 URL 格式无效: %w", err)
	}
	scheme = strings.ToLower(strings.TrimSpace(u.Scheme))
	defPort, ok := supportedSchemes[scheme]
	if !ok {
		return "", scheme, false, fmt.Errorf("不支持的代理协议 %q（仅支持 http/https/socks5/socks5h）", scheme)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return "", scheme, false, fmt.Errorf("代理地址缺少主机名")
	}
	port := strings.TrimSpace(u.Port())
	if port == "" {
		port = defPort
	} else {
		pNum, errPort := strconv.Atoi(port)
		if errPort != nil || pNum < 1 || pNum > 65535 {
			return "", scheme, false, fmt.Errorf("代理端口号无效: %s", port)
		}
	}

	u.Scheme = scheme
	u.Host = net.JoinHostPort(strings.ToLower(host), port)
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	if u.User != nil {
		if _, set := u.User.Password(); set {
			hasPassword = true
		}
	}
	return u.String(), scheme, hasPassword, nil
}

// MaskProxyURL 返回隐藏认证密码后的代理地址用于界面和日志展示。
func MaskProxyURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	if u.User == nil {
		return u.String()
	}
	user := u.User.Username()
	if _, hasPass := u.User.Password(); hasPass {
		return fmt.Sprintf("%s://%s:***@%s", u.Scheme, user, u.Host)
	}
	return u.String()
}

// HasPassword 判断代理 URL 是否包含密码字段。
func HasPassword(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return false
	}
	_, set := u.User.Password()
	return set
}

// ProtocolOf 提取代理 URL 的协议名。
func ProtocolOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Scheme))
}

// ParseBulkInput 自动识别并解析多行 URI、Base64 订阅内容或 Clash YAML 节点列表。
func ParseBulkInput(text string) []ParsedEntry {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if decoded, ok := tryDecodeBase64Subscription(text); ok {
		text = decoded
	}
	if looksLikeClashYAML(text) {
		return parseClashYAMLProxies(text)
	}
	return parsePlainURILines(text)
}

func tryDecodeBase64Subscription(raw string) (string, bool) {
	if strings.Contains(raw, "://") || strings.Contains(strings.ToLower(raw), "proxies:") {
		return "", false
	}
	compact := strings.Join(strings.Fields(raw), "")
	if len(compact) < 8 {
		return "", false
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		b, err := enc.DecodeString(compact)
		if err != nil {
			continue
		}
		decoded := string(b)
		if strings.Contains(decoded, "://") || strings.Contains(strings.ToLower(decoded), "proxies:") {
			return decoded, true
		}
	}
	return "", false
}

func parsePlainURILines(text string) []ParsedEntry {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]ParsedEntry, 0, len(lines))
	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// 分离可选的 #名称 备注
		name := ""
		uriPart := trimmed
		if idx := strings.Index(trimmed, "#"); idx >= 0 {
			rawName := strings.TrimSpace(trimmed[idx+1:])
			if unesc, err := url.QueryUnescape(rawName); err == nil {
				name = strings.TrimSpace(unesc)
			} else {
				name = rawName
			}
			uriPart = strings.TrimSpace(trimmed[:idx])
		}

		schemeIdx := strings.Index(uriPart, "://")
		if schemeIdx <= 0 {
			out = append(out, ParsedEntry{
				Line:   lineNum,
				Name:   name,
				Reason: "格式无效：缺少协议前缀（如 http:// 或 socks5://）",
			})
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(uriPart[:schemeIdx]))
		if unsupportedSchemes[scheme] {
			out = append(out, ParsedEntry{
				Line:        lineNum,
				Name:        name,
				Protocol:    scheme,
				Unsupported: true,
				Reason:      fmt.Sprintf("暂不支持 %s 协议，请使用本地客户端转为 http/socks5 后导入", scheme),
			})
			continue
		}

		norm, proto, _, err := NormalizeProxyURL(uriPart)
		if err != nil {
			out = append(out, ParsedEntry{
				Line:     lineNum,
				Name:     name,
				Protocol: scheme,
				Reason:   err.Error(),
			})
			continue
		}
		if name == "" {
			if u, errU := url.Parse(norm); errU == nil {
				name = u.Host
			}
		}
		out = append(out, ParsedEntry{
			Line:       lineNum,
			Name:       name,
			Protocol:   proto,
			Normalized: norm,
		})
	}
	return out
}

func looksLikeClashYAML(text string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "proxies:") {
			rest := strings.TrimSpace(strings.TrimPrefix(t, "proxies:"))
			if rest == "" || strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, "[") {
				return true
			}
		}
	}
	return false
}

// parseClashYAMLProxies 纯标准库解析 Clash YAML 的 proxies 节点段，支持块式和流式对象两种写法。
func parseClashYAMLProxies(text string) []ParsedEntry {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	inProxies := false
	baseIndent := 0
	var out []ParsedEntry

	var current map[string]string
	currentLine := 0

	flushBlock := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, convertClashNodeFields(currentLine, current))
		current = nil
		currentLine = 0
	}

	for i, rawLine := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " \t"))

		if !inProxies {
			if strings.HasPrefix(trimmed, "proxies:") {
				inProxies = true
				baseIndent = indent
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "proxies:"))
				if strings.HasPrefix(rest, "[") {
					for _, obj := range splitFlowObjects(rest) {
						fields := parseFlowMapping(obj)
						if len(fields) > 0 {
							out = append(out, convertClashNodeFields(lineNum, fields))
						}
					}
					if strings.Contains(rest, "]") {
						inProxies = false
					}
				}
			}
			continue
		}

		// 遇到与 proxies: 同级或更小缩进且不是列表项的新顶层段，结束 proxies 段
		if indent <= baseIndent && !strings.HasPrefix(trimmed, "-") {
			flushBlock()
			break
		}

		if strings.HasPrefix(trimmed, "-") {
			flushBlock()
			afterDash := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			// 流式写法：- {name: hk, type: socks5, server: 1.2.3.4, port: 1080}
			if strings.HasPrefix(afterDash, "{") {
				fields := parseFlowMapping(afterDash)
				if len(fields) > 0 {
					out = append(out, convertClashNodeFields(lineNum, fields))
				}
				continue
			}
			current = make(map[string]string)
			currentLine = lineNum
			if k, v, ok := splitYAMLKV(afterDash); ok {
				current[k] = v
			}
			continue
		}

		// 块式条目的后续字段行
		if current != nil {
			if k, v, ok := splitYAMLKV(trimmed); ok {
				current[k] = v
			}
		}
	}
	flushBlock()
	return out
}

func convertClashNodeFields(lineNum int, fields map[string]string) ParsedEntry {
	name := strings.TrimSpace(fields["name"])
	typ := strings.ToLower(strings.TrimSpace(fields["type"]))
	server := strings.TrimSpace(fields["server"])
	port := strings.TrimSpace(fields["port"])
	user := strings.TrimSpace(fields["username"])
	pass := strings.TrimSpace(fields["password"])
	tlsOn := strings.EqualFold(strings.TrimSpace(fields["tls"]), "true")

	if typ == "" {
		return ParsedEntry{Line: lineNum, Name: name, Reason: "Clash 节点缺少 type 字段"}
	}
	if unsupportedSchemes[typ] {
		return ParsedEntry{
			Line:        lineNum,
			Name:        name,
			Protocol:    typ,
			Unsupported: true,
			Reason:      fmt.Sprintf("暂不支持 Clash %s 节点，请仅导入 http/https/socks5 节点", typ),
		}
	}

	scheme := typ
	if scheme == "http" && tlsOn {
		scheme = "https"
	}
	if _, ok := supportedSchemes[scheme]; !ok {
		return ParsedEntry{
			Line:        lineNum,
			Name:        name,
			Protocol:    typ,
			Unsupported: true,
			Reason:      fmt.Sprintf("暂不支持 Clash 节点类型 %s", typ),
		}
	}
	if server == "" || port == "" {
		return ParsedEntry{Line: lineNum, Name: name, Protocol: scheme, Reason: "Clash 节点缺少 server 或 port"}
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(server, port),
	}
	if user != "" || pass != "" {
		if pass != "" {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}
	norm, proto, _, err := NormalizeProxyURL(u.String())
	if err != nil {
		return ParsedEntry{Line: lineNum, Name: name, Protocol: scheme, Reason: err.Error()}
	}
	if name == "" {
		name = net.JoinHostPort(server, port)
	}
	return ParsedEntry{
		Line:       lineNum,
		Name:       name,
		Protocol:   proto,
		Normalized: norm,
	}
}

func splitYAMLKV(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(line[:idx]))
	val := strings.TrimSpace(line[idx+1:])
	val = stripInlineYAMLComment(val)
	val = unquoteYAML(val)
	return key, val, true
}

func stripInlineYAMLComment(val string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(val); i++ {
		switch val[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || val[i-1] == ' ' || val[i-1] == '\t') {
				return strings.TrimSpace(val[:i])
			}
		}
	}
	return strings.TrimSpace(val)
}

func unquoteYAML(val string) string {
	val = strings.TrimSpace(val)
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

func splitFlowObjects(s string) []string {
	var objs []string
	depth := 0
	start := -1
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && (i == 0 || s[i-1] != '\\') {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 && start >= 0 {
				objs = append(objs, s[start:i+1])
				start = -1
			}
		}
	}
	return objs
}

func parseFlowMapping(obj string) map[string]string {
	s := strings.TrimSpace(obj)
	s = strings.TrimPrefix(s, "{")
	if idx := strings.LastIndex(s, "}"); idx >= 0 {
		s = s[:idx]
	}
	parts := splitTopLevelComma(s)
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		if k, v, ok := splitYAMLKV(strings.TrimSpace(p)); ok {
			out[k] = v
		}
	}
	return out
}

func splitTopLevelComma(s string) []string {
	var parts []string
	depth := 0
	inQuote := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote != 0 {
			if ch == inQuote && (i == 0 || s[i-1] != '\\') {
				inQuote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inQuote = ch
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}
