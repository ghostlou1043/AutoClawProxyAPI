//go:build !windows

package banner

// EnableANSI 在类 Unix 终端中默认支持 ANSI 转义序列，无需额外初始化。
func EnableANSI() {}
