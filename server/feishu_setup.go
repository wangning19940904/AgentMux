package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	feishuAccountsBaseURL = "https://accounts.feishu.cn"
	larkAccountsBaseURL   = "https://accounts.larksuite.com"
)

func (s *Server) handleFeishuSetupBegin(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 15 * time.Second}

	initResp, err := feishuRegistrationCall(client, feishuAccountsBaseURL, "init", nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "feishu init: " + err.Error()})
		return
	}
	if errMsg, _ := initResp["error"].(string); errMsg != "" {
		desc, _ := initResp["error_description"].(string)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("feishu init: %s: %s", errMsg, desc)})
		return
	}

	beginResp, err := feishuRegistrationCall(client, feishuAccountsBaseURL, "begin", map[string]string{
		"archetype":         "PersonalAgent",
		"auth_method":       "client_secret",
		"request_user_info": "open_id",
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "feishu begin: " + err.Error()})
		return
	}
	if errMsg, _ := beginResp["error"].(string); errMsg != "" {
		desc, _ := beginResp["error_description"].(string)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("feishu begin: %s: %s", errMsg, desc)})
		return
	}

	deviceCode, _ := beginResp["device_code"].(string)
	qrURL, _ := beginResp["verification_uri_complete"].(string)
	interval, _ := beginResp["interval"].(float64)
	expiresIn, _ := beginResp["expire_in"].(float64)
	if deviceCode == "" || qrURL == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "feishu begin: incomplete response"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code": deviceCode,
		"qr_url":      qrURL,
		"interval":    int(interval),
		"expires_in":  int(expiresIn),
	})
}

func (s *Server) handleFeishuSetupPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
		BaseURL    string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_code required"})
		return
	}

	baseURL, err := normalizeFeishuAccountsBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}

	for attempt := 0; attempt < 2; attempt++ {
		pollResp, err := feishuRegistrationCall(client, baseURL, "poll", map[string]string{
			"device_code": req.DeviceCode,
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "feishu poll: " + err.Error()})
			return
		}

		if userInfo, ok := pollResp["user_info"].(map[string]any); ok {
			if brand, _ := userInfo["tenant_brand"].(string); strings.EqualFold(brand, "lark") && baseURL != larkAccountsBaseURL {
				baseURL = larkAccountsBaseURL
				continue
			}
		}

		result := map[string]any{
			"status":   "pending",
			"base_url": baseURL,
		}

		clientID, _ := pollResp["client_id"].(string)
		clientSecret, _ := pollResp["client_secret"].(string)
		if clientID != "" && clientSecret != "" {
			platform := "feishu"
			if userInfo, ok := pollResp["user_info"].(map[string]any); ok {
				if brand, _ := userInfo["tenant_brand"].(string); strings.EqualFold(brand, "lark") {
					platform = "lark"
				}
				if oid, _ := userInfo["open_id"].(string); oid != "" {
					result["owner_open_id"] = oid
				}
			}
			result["status"] = "completed"
			result["app_id"] = clientID
			result["app_secret"] = clientSecret
			result["platform"] = platform
			writeJSON(w, http.StatusOK, result)
			return
		}

		if errCode, _ := pollResp["error"].(string); errCode != "" {
			switch errCode {
			case "authorization_pending":
			case "slow_down":
				result["slow_down"] = true
			case "access_denied":
				result["status"] = "denied"
			case "expired_token":
				result["status"] = "expired"
			default:
				desc, _ := pollResp["error_description"].(string)
				result["status"] = "error"
				result["error"] = fmt.Sprintf("%s: %s", errCode, desc)
			}
		}

		writeJSON(w, http.StatusOK, result)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "base_url": baseURL})
}

func normalizeFeishuAccountsBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return feishuAccountsBaseURL, nil
	}
	if raw == feishuAccountsBaseURL || raw == larkAccountsBaseURL {
		return raw, nil
	}
	return "", fmt.Errorf("unsupported feishu setup base_url %q", raw)
}

func feishuRegistrationCall(client *http.Client, baseURL, action string, params map[string]string) (map[string]any, error) {
	form := url.Values{}
	form.Set("action", action)
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	return out, nil
}
