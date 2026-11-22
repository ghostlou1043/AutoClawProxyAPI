package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level 表示日志级别。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel 将字符串转换为日志级别。
func ParseLevel(raw string) Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// String 返回日志级别文本标识。
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Entry 是一条结构化日志记录。
type Entry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Module  string `json:"module"`
	Message string `json:"message"`
}

// Logger 提供线程安全的控制台日志输出与内存环形查询缓冲。
type Logger struct {
	mu      sync.RWMutex
	level   Level
	out     io.Writer
	entries []Entry
	maxSize int
}

// New 创建日志器实例。
func New(level string, maxEntries int) *Logger {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	return &Logger{
		level:   ParseLevel(level),
		out:     os.Stdout,
		entries: make([]Entry, 0, maxEntries),
		maxSize: maxEntries,
	}
}

// SetLevel 动态调整日志输出级别。
func (l *Logger) SetLevel(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = ParseLevel(level)
}

// LevelString 返回当前生效的日志级别小写名称。
func (l *Logger) LevelString() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return strings.ToLower(l.level.String())
}

// Debug 记录调试级别日志。
func (l *Logger) Debug(module, format string, args ...any) {
	l.log(LevelDebug, module, format, args...)
}

// Info 记录常规运行日志。
func (l *Logger) Info(module, format string, args ...any) {
	l.log(LevelInfo, module, format, args...)
}

// Warn 记录告警日志。
func (l *Logger) Warn(module, format string, args ...any) {
	l.log(LevelWarn, module, format, args...)
}

// Error 记录错误日志。
func (l *Logger) Error(module, format string, args ...any) {
	l.log(LevelError, module, format, args...)
}

func (l *Logger) log(lv Level, module, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lv < l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	now := time.Now().Format("2006-01-02 15:04:05")
	if strings.TrimSpace(module) == "" {
		module = "System"
	}
	entry := Entry{
		Time:    now,
		Level:   lv.String(),
		Module:  module,
		Message: msg,
	}
	if len(l.entries) >= l.maxSize {
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = entry
	} else {
		l.entries = append(l.entries, entry)
	}
	_, _ = fmt.Fprintf(l.out, "%s [%s] [%s] %s\n", now, lv.String(), module, msg)
}

// Recent 返回最近 limit 条日志（按时间升序）。
func (l *Logger) Recent(limit int) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	total := len(l.entries)
	if limit <= 0 || limit > total {
		limit = total
	}
	start := total - limit
	out := make([]Entry, limit)
	copy(out, l.entries[start:])
	return out
}

// Clear 清空内存中的历史日志记录。
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
}
