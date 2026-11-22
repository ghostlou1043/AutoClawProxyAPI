package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPanelPasswordHashAndVerify(t *testing.T) {
	if _, err := HashPanelPassword("123"); err == nil {
		t.Fatalf("少于 4 位的密码应被拒绝")
	}
	hash, err := HashPanelPassword("vilk-secret-2026")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	if !VerifyPanelPassword("vilk-secret-2026", hash) {
		t.Fatalf("正确密码应校验通过")
	}
	if VerifyPanelPassword("wrong-password", hash) {
		t.Fatalf("错误密码不应校验通过")
	}
}

func TestVKeyGenerationAndValidation(t *testing.T) {
	k1 := GenerateVKey()
	k2 := GenerateVKey()
	if !strings.HasPrefix(k1, "vKey-") || len(k1) < 20 {
		t.Fatalf("生成的密钥格式不符: %s", k1)
	}
	if k1 == k2 {
		t.Fatalf("两次随机生成的 vKey 不应相同")
	}

	cfg := Default()
	if enabled, ok := cfg.ValidateClientAPIKey(""); enabled || !ok {
		t.Fatalf("未配置 API Key 时应免鉴权放行")
	}

	cfg.APIKeys = []APIKeyItem{
		{ID: "k1", Name: "启用密钥", Key: k1, Enabled: true},
		{ID: "k2", Name: "停用密钥", Key: k2, Enabled: false},
	}
	if enabled, ok := cfg.ValidateClientAPIKey(k1); !enabled || !ok {
		t.Fatalf("已启用的 vKey 应校验通过")
	}
	if enabled, ok := cfg.ValidateClientAPIKey(k2); !enabled || ok {
		t.Fatalf("已停用的 vKey 不应校验通过")
	}
	if enabled, ok := cfg.ValidateClientAPIKey(""); !enabled || ok {
		t.Fatalf("配置密钥后空密钥应被拦截")
	}
}

func TestLoadCreatesDefaultAndMigratesLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 测试旧版字符串数组格式平滑迁移
	legacy := `{
		"listen": "127.0.0.1:9090",
		"api_keys": ["sk-legacy-1", "sk-legacy-1", "sk-legacy-2"],
		"routing": {
			"aliases": {
				"default": "glm-5.3-flash",
				"mix": ["autoclawcn:glm-2", "autoclawglobal:openclaw"]
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("加载旧版配置失败: %v", err)
	}
	cfg := mgr.Get()
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("旧版 API Key 去重迁移异常: %+v", cfg.APIKeys)
	}
	if !cfg.APIKeys[0].Enabled || cfg.APIKeys[0].Key != "sk-legacy-1" {
		t.Fatalf("迁移后的 APIKeyItem 异常: %+v", cfg.APIKeys[0])
	}
	if len(cfg.Routing.Aliases["default"]) != 1 || len(cfg.Routing.Aliases["mix"]) != 2 {
		t.Fatalf("别名解析异常: %+v", cfg.Routing.Aliases)
	}
}
