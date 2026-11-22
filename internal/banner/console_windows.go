//go:build windows

package banner

import (
	"os"
	"syscall"
	"unsafe"
)

const enableVirtualTerminalProcessing = 0x0004

// EnableANSI 在 Windows 10/11 控制台中开启虚拟终端 ANSI 转义序列支持。
func EnableANSI() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode := kernel32.NewProc("GetConsoleMode")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")

	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		handle := syscall.Handle(f.Fd())
		var mode uint32
		r1, _, _ := getConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
		if r1 == 0 {
			continue
		}
		if mode&enableVirtualTerminalProcessing == 0 {
			_, _, _ = setConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
		}
	}
}
