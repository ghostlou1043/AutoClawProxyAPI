package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"autoclaw2api/internal/proxypool"
)

func (s *Server) handleAdminProxiesSnapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.proxyMgr.Snapshot())
}

func (s *Server) handleAdminProxiesImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求参数解析失败"})
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请粘贴代理 URI、Base64 订阅或 Clash YAML 内容"})
		return
	}
	summary, err := s.proxyMgr.ImportBulk(r.Context(), body.Text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "导入代理节点失败: " + err.Error()})
		return
	}
	if s.logger != nil {
		s.logger.Info("ProxyPool", "批量导入代理完成：新增 %d / 重复 %d / 不支持 %d / 无效 %d",
			summary.Added, summary.Duplicate, summary.Unsupported, summary.Invalid)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": fmt.Sprintf("导入完成：新增 %d 条，重复 %d 条，不支持 %d 条，无效 %d 条",
			summary.Added, summary.Duplicate, summary.Unsupported, summary.Invalid),
		"summary":  summary,
		"snapshot": s.proxyMgr.Snapshot(),
	})
}

func (s *Server) handleAdminProxyUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Name    *string `json:"name"`
		URL     *string `json:"url"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求参数解析失败"})
		return
	}
	view, err := s.proxyMgr.UpdateNode(id, body.Name, body.URL, body.Enabled)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, proxypool.ErrNodeNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "代理节点已更新并重建连接池",
		"node":    view,
	})
}

func (s *Server) handleAdminProxyDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.proxyMgr.DeleteNode(id); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, proxypool.ErrNodeNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, proxypool.ErrNodeBound) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "代理节点已删除"})
}

func (s *Server) handleAdminProxyProbeOne(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	view, err := s.proxyMgr.ProbeNode(r.Context(), id)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, proxypool.ErrNodeNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	msg := fmt.Sprintf("测活完成：%s（%dms）", view.Status, view.LatencyMS)
	if view.Status == proxypool.StatusUnhealthy && view.LastError != "" {
		msg = "测活异常：" + view.LastError
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": msg,
		"node":    view,
	})
}

func (s *Server) handleAdminProxyProbeAll(w http.ResponseWriter, r *http.Request) {
	count := s.proxyMgr.ProbeAll(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"message":  fmt.Sprintf("全部测活完成，共检测 %d 个已启用节点", count),
		"checked":  count,
		"snapshot": s.proxyMgr.Snapshot(),
	})
}

func (s *Server) handleAdminProxyHealthConfig(w http.ResponseWriter, r *http.Request) {
	var body proxypool.HealthConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求参数解析失败"})
		return
	}
	updated, err := s.proxyMgr.UpdateHealthConfig(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "后台测活配置已保存",
		"health":  updated,
	})
}

func (s *Server) handleAdminProxyRegionBinding(w http.ResponseWriter, r *http.Request) {
	region := strings.TrimSpace(r.PathValue("region"))
	var body proxypool.Binding
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求参数解析失败"})
		return
	}
	if err := s.proxyMgr.SetRegionBinding(region, body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if s.logger != nil {
		s.logger.Info("ProxyPool", "区域 [%s] 默认出口已更新为 mode=%s proxy_id=%s", region, body.Mode, body.ProxyID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":  "区域默认出口已更新",
		"snapshot": s.proxyMgr.Snapshot(),
	})
}

func (s *Server) handleAdminAccountProxyBinding(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	acc, ok := s.pool.GetByID(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "账号不存在"})
		return
	}
	var body proxypool.Binding
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求参数解析失败"})
		return
	}
	if err := s.proxyMgr.SetAccountBinding(id, body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	route := s.proxyMgr.DescribeAccountRoute(id, string(acc.Cred.Region))
	if s.logger != nil {
		s.logger.Info("ProxyPool", "账号 [%s] 出口已更新为：%s", acc.Cred.DisplayLabel(), route.Summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "账号代理出口已保存：" + route.Summary,
		"route":   route,
	})
}
