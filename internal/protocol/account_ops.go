package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RefreshResult 保存 Token 刷新返回的新凭证信息。
type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// RefreshToken 调用 POST /userapi/v1/refresh 刷新访问令牌。
// 若上游未提供明确过期时间，按 28 天保守记录。
func RefreshToken(ctx context.Context, client *http.Client, baseURL, accessToken, refreshToken, deviceID string) (*RefreshResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, &UpstreamError{
			HTTPStatus: http.StatusUnauthorized,
			Kind:       ErrorKindSessionDead,
			Message:    "凭证缺少 refreshToken，无法自动续期",
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	payload, _ := json.Marshal(map[string]any{
		"source_id":     SourceID,
		"device_id":     strings.TrimSpace(deviceID),
		"refresh_token": strings.TrimSpace(refreshToken),
	})
	endpoint := strings.TrimRight(baseURL, "/") + PathRefreshToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造刷新请求失败: %w", err)
	}
	AttachUserHeaders(req, accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, &UpstreamError{
			Kind:    ErrorKindTransient,
			Message: "调用 Token 刷新接口失败: " + err.Error(),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, &UpstreamError{
			HTTPStatus: resp.StatusCode,
			Kind:       ErrorKindTransient,
			Message:    "读取 Token 刷新响应失败: " + err.Error(),
		}
	}

	var env struct {
		Code *int64 `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, ClassifyError(resp.StatusCode, 0, string(raw), retryAfter)
	}
	var bizCode int64
	if env.Code != nil {
		bizCode = *env.Code
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ClassifyError(resp.StatusCode, bizCode, string(raw), retryAfter)
	}
	if bizCode != 0 {
		return nil, ClassifyError(resp.StatusCode, bizCode, env.Msg, retryAfter)
	}
	newAccess := strings.TrimSpace(env.Data.AccessToken)
	if newAccess == "" {
		return nil, &UpstreamError{
			HTTPStatus: resp.StatusCode,
			Kind:       ErrorKindSessionDead,
			Message:    "Token 刷新成功但上游未返回 access_token",
		}
	}
	expiresAt := JWTExpirySeconds(newAccess)
	if expiresAt == 0 {
		if env.Data.ExpiresIn > 0 {
			expiresAt = time.Now().Add(time.Duration(env.Data.ExpiresIn) * time.Second).Unix()
		} else {
			expiresAt = time.Now().Add(DefaultTokenTTL).Unix()
		}
	}
	return &RefreshResult{
		AccessToken:  newAccess,
		RefreshToken: strings.TrimSpace(env.Data.RefreshToken),
		ExpiresAt:    expiresAt,
	}, nil
}

// FetchWalletBalance 查询账号钱包可用积分余额。
func FetchWalletBalance(ctx context.Context, client *http.Client, baseURL, token string) (float64, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	endpoint := strings.TrimRight(baseURL, "/") + PathWallets
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("构造余额查询请求失败: %w", err)
	}
	AttachUserHeaders(req, token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, &UpstreamError{
			Kind:    ErrorKindTransient,
			Message: "查询钱包积分失败: " + err.Error(),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return 0, &UpstreamError{
			HTTPStatus: resp.StatusCode,
			Kind:       ErrorKindTransient,
			Message:    "读取钱包积分响应失败: " + err.Error(),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, ClassifyError(resp.StatusCode, 0, string(raw), retryAfter)
	}
	var env struct {
		Code *int64 `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TotalBalance any `json:"total_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, ClassifyError(resp.StatusCode, 0, string(raw), retryAfter)
	}
	if env.Code != nil && *env.Code != 0 {
		return 0, ClassifyError(resp.StatusCode, *env.Code, env.Msg, retryAfter)
	}
	return FloatFromAny(env.Data.TotalBalance), nil
}

// CheckinOutcome 描述单次每日签到执行结果。
type CheckinOutcome struct {
	Claimed          bool
	AlreadyCompleted bool
	Points           int64
	Message          string
}

// DailyCheckin 执行每日签到（POST /autoclaw-proxy/proxy/autoclaw-task-complete）。
func DailyCheckin(ctx context.Context, client *http.Client, baseURL, token string) (*CheckinOutcome, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	payload, _ := json.Marshal(map[string]string{"task_id": "daily_signin"})
	endpoint := strings.TrimRight(baseURL, "/") + PathDailyCheckin
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造签到请求失败: %w", err)
	}
	AttachUserHeaders(req, token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, &UpstreamError{
			Kind:    ErrorKindTransient,
			Message: "签到请求失败: " + err.Error(),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, &UpstreamError{
			HTTPStatus: resp.StatusCode,
			Kind:       ErrorKindTransient,
			Message:    "读取签到响应失败: " + err.Error(),
		}
	}
	var env struct {
		Code *int64 `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Success          bool   `json:"success"`
			AlreadyCompleted bool   `json:"already_completed"`
			RewardPoints     any    `json:"reward_points"`
			TaskID           string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, ClassifyError(resp.StatusCode, 0, string(raw), retryAfter)
	}
	points := int64(FloatFromAny(env.Data.RewardPoints))

	if env.Code != nil && *env.Code != 0 {
		if IsDeadBizCode(*env.Code) {
			return nil, ClassifyError(http.StatusUnauthorized, *env.Code, env.Msg, retryAfter)
		}
		if resp.StatusCode >= 400 {
			return nil, ClassifyError(resp.StatusCode, *env.Code, env.Msg, retryAfter)
		}
		if containsSessionDeadPhrase(env.Msg) {
			return nil, ClassifyError(http.StatusUnauthorized, *env.Code, env.Msg, retryAfter)
		}
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = "签到未完成"
		}
		return &CheckinOutcome{
			AlreadyCompleted: env.Data.AlreadyCompleted,
			Message:          msg,
		}, nil
	}

	claimed := points > 0 || (env.Data.Success && !env.Data.AlreadyCompleted)
	if resp.StatusCode >= 400 && !claimed && !env.Data.AlreadyCompleted {
		return nil, ClassifyError(resp.StatusCode, 0, string(raw), retryAfter)
	}
	msg := strings.TrimSpace(env.Msg)
	switch {
	case env.Data.AlreadyCompleted:
		msg = "今日已签到（幂等无重复奖励）"
	case claimed:
		if points > 0 {
			msg = fmt.Sprintf("签到成功，获得 +%d 积分", points)
		} else {
			msg = "签到成功"
		}
	case msg == "":
		msg = "签到未完成（上游未发放积分）"
	}
	return &CheckinOutcome{
		Claimed:          claimed,
		AlreadyCompleted: env.Data.AlreadyCompleted,
		Points:           points,
		Message:          msg,
	}, nil
}

// ReportLoginActive 在签到成功后调用 POST /userapi/v1/user-info 上报登录活跃。
func ReportLoginActive(ctx context.Context, client *http.Client, baseURL, token, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	payload, _ := json.Marshal(map[string]string{
		"source_id": SourceID,
		"device_id": deviceID,
	})
	endpoint := strings.TrimRight(baseURL, "/") + PathUserInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	AttachUserHeaders(req, token)

	resp, err := client.Do(req)
	if err != nil {
		return &UpstreamError{
			Kind:    ErrorKindTransient,
			Message: "上报登录活跃失败: " + err.Error(),
		}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode >= 400 {
		return ClassifyError(resp.StatusCode, 0, string(raw), 0)
	}
	return nil
}
