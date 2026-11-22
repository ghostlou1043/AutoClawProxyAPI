package netx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"time"
)

// NewSOCKS5Dialer 根据 socks5:// 或 socks5h:// URL 构造支持上下文拨号的纯标准库拨号器。
func NewSOCKS5Dialer(u *url.URL) (*SOCKS5Dialer, error) {
	return newSOCKS5Dialer(u)
}

// SOCKS5Dialer 是公开的 SOCKS5 拨号器类型别名。
type SOCKS5Dialer = socks5Dialer

// socks5Dialer 使用纯 Go 标准库实现 RFC 1928 / RFC 1929 SOCKS5 代理拨号。
type socks5Dialer struct {
	proxyAddr string
	username  string
	password  string
	dialer    *net.Dialer
}

func newSOCKS5Dialer(u *url.URL) (*socks5Dialer, error) {
	host := u.Host
	if host == "" {
		return nil, errors.New("SOCKS5 代理缺少主机地址")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1080")
	}
	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return &socks5Dialer{
		proxyAddr: host,
		username:  user,
		password:  pass,
		dialer: &net.Dialer{
			Timeout:   20 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}, nil
}

// DialContext 通过 SOCKS5 代理建立到目标地址的 TCP 连接。
func (d *socks5Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("SOCKS5 不支持的网络协议: %s", network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("解析目标地址失败: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("无效的目标端口: %s", portStr)
	}

	conn, err := d.dialer.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("连接 SOCKS5 代理失败: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := d.handshake(conn, host, uint16(port)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks5Dialer) handshake(conn net.Conn, host string, port uint16) error {
	// 1. 认证方式协商
	methods := []byte{0x00}
	if d.username != "" {
		methods = append(methods, 0x02)
	}
	greeting := make([]byte, 2+len(methods))
	greeting[0] = 0x05
	greeting[1] = byte(len(methods))
	copy(greeting[2:], methods)
	if _, err := conn.Write(greeting); err != nil {
		return fmt.Errorf("SOCKS5 握手发送失败: %w", err)
	}

	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("SOCKS5 握手读取失败: %w", err)
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("SOCKS5 协议版本不匹配: %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
		// 无需认证
	case 0x02:
		if err := d.authenticate(conn); err != nil {
			return err
		}
	case 0xFF:
		return errors.New("SOCKS5 代理拒绝支持的认证方式")
	default:
		return fmt.Errorf("SOCKS5 代理返回未知认证方式: %d", resp[1])
	}

	// 2. 发送 CONNECT 请求
	req := make([]byte, 0, 7+len(host))
	req = append(req, 0x05, 0x01, 0x00)
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("SOCKS5 目标域名长度超限")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	req = append(req, portBuf[:]...)

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("SOCKS5 发送连接请求失败: %w", err)
	}

	// 3. 解析代理响应
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("SOCKS5 读取连接响应失败: %w", err)
	}
	if head[0] != 0x05 {
		return fmt.Errorf("SOCKS5 响应版本异常: %d", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("SOCKS5 连接目标被拒绝（错误码 0x%02x）", head[1])
	}

	var addrLen int
	switch head[3] {
	case 0x01:
		addrLen = 4
	case 0x04:
		addrLen = 16
	case 0x03:
		var lb [1]byte
		if _, err := io.ReadFull(conn, lb[:]); err != nil {
			return fmt.Errorf("SOCKS5 读取绑定域名长度失败: %w", err)
		}
		addrLen = int(lb[0])
	default:
		return fmt.Errorf("SOCKS5 未知地址类型: %d", head[3])
	}
	discard := make([]byte, addrLen+2)
	if _, err := io.ReadFull(conn, discard); err != nil {
		return fmt.Errorf("SOCKS5 读取绑定地址失败: %w", err)
	}
	return nil
}

func (d *socks5Dialer) authenticate(conn net.Conn) error {
	if len(d.username) > 255 || len(d.password) > 255 {
		return errors.New("SOCKS5 用户名或密码长度超限")
	}
	buf := make([]byte, 0, 3+len(d.username)+len(d.password))
	buf = append(buf, 0x01, byte(len(d.username)))
	buf = append(buf, d.username...)
	buf = append(buf, byte(len(d.password)))
	buf = append(buf, d.password...)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("SOCKS5 认证发送失败: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("SOCKS5 认证响应读取失败: %w", err)
	}
	if resp[1] != 0x00 {
		return errors.New("SOCKS5 用户名或密码认证失败")
	}
	return nil
}
