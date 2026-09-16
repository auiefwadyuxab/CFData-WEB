package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	subscriptionStoreFile = "subscriptions.json"
	subscriptionMaxBytes  = 32 << 20
)

type subscription struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	Content       string            `json:"content,omitempty"`
	Status        string            `json:"status"`
	Error         string            `json:"error,omitempty"`
	StatusCode    int               `json:"statusCode,omitempty"`
	ContentType   string            `json:"contentType,omitempty"`
	ContentLength int64             `json:"contentLength,omitempty"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
	CreatedAt     string            `json:"createdAt"`
}

type subscriptionSaveRequest struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type subscriptionUpdateRequest struct {
	ID string `json:"id"`
}

type subscriptionIDRequest struct {
	ID string `json:"id"`
}

type subscriptionSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	ContentType   string `json:"contentType,omitempty"`
	ContentLength int64  `json:"contentLength,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	CreatedAt     string `json:"createdAt"`
	HasContent    bool   `json:"hasContent"`
}

var subscriptionStore struct {
	sync.Mutex
	Loaded bool
	Items  []subscription
}

func subscriptionFilePath() string {
	return filepath.Join(filepath.Dir(os.Args[0]), subscriptionStoreFile)
}

func ensureSubscriptionStoreLoaded() error {
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	if subscriptionStore.Loaded {
		return nil
	}

	path := subscriptionFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			subscriptionStore.Items = nil
			subscriptionStore.Loaded = true
			return nil
		}
		return fmt.Errorf("读取订阅存储失败: %w", err)
	}

	if len(raw) == 0 {
		subscriptionStore.Items = nil
		subscriptionStore.Loaded = true
		return nil
	}

	var items []subscription
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", subscriptionStoreFile, err)
	}
	for i := range items {
		if items[i].Headers == nil {
			items[i].Headers = map[string]string{}
		}
		if items[i].Status == "" {
			items[i].Status = "未更新"
		}
	}
	subscriptionStore.Items = items
	subscriptionStore.Loaded = true
	return nil
}

func saveSubscriptionStoreLocked() error {
	path := subscriptionFilePath()
	raw, err := json.MarshalIndent(subscriptionStore.Items, "", "  ")
	if err != nil {
		return fmt.Errorf("编码订阅存储失败: %w", err)
	}
	if err := atomicWriteFile(path, raw, 0644); err != nil {
		return fmt.Errorf("保存订阅存储失败: %w", err)
	}
	return nil
}

func subscriptionSummaryOf(item subscription) subscriptionSummary {
	return subscriptionSummary{
		ID:            item.ID,
		Name:          item.Name,
		URL:           item.URL,
		Status:        item.Status,
		Error:         item.Error,
		StatusCode:    item.StatusCode,
		ContentType:   item.ContentType,
		ContentLength: item.ContentLength,
		UpdatedAt:     item.UpdatedAt,
		CreatedAt:     item.CreatedAt,
		HasContent:    item.Content != "",
	}
}

func listSubscriptions() ([]subscriptionSummary, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	items := make([]subscriptionSummary, 0, len(subscriptionStore.Items))
	for _, item := range subscriptionStore.Items {
		items = append(items, subscriptionSummaryOf(item))
	}
	return items, nil
}

func getSubscription(id string) (*subscription, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID == id {
			item := subscriptionStore.Items[i]
			return &item, nil
		}
	}
	return nil, fmt.Errorf("未找到订阅: %s", id)
}

func normalizeSubscriptionHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func validateSubscriptionURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("订阅 URL 不能为空")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("订阅 URL 无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("订阅 URL 仅支持 HTTP/HTTPS")
	}
	return u.String(), nil
}

func fetchSubscription(ctx context.Context, item subscription) (body []byte, statusCode int, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	for key, value := range item.Headers {
		req.Header.Set(key, value)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "v2rayNG/2.2.6")
	}
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip")
	}

	resp, err := upstreamHTTPClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	contentType = resp.Header.Get("Content-Type")
	statusCode = resp.StatusCode

	var reader io.Reader = resp.Body
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			return nil, statusCode, contentType, fmt.Errorf("解压 gzip 订阅失败: %w", gzErr)
		}
		defer gz.Close()
		reader = gz
	}

	limited := io.LimitReader(reader, subscriptionMaxBytes+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, statusCode, contentType, err
	}
	if len(body) > subscriptionMaxBytes {
		return nil, statusCode, contentType, fmt.Errorf("订阅内容超过 %d MB 限制", subscriptionMaxBytes>>20)
	}
	return body, statusCode, contentType, nil
}

func createOrUpdateSubscription(params subscriptionSaveRequest) (*subscription, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, fmt.Errorf("订阅名称不能为空")
	}
	urlValue, err := validateSubscriptionURL(params.URL)
	if err != nil {
		return nil, err
	}
	headers := normalizeSubscriptionHeaders(params.Headers)

	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	id := strings.TrimSpace(params.ID)
	if id != "" {
		for i := range subscriptionStore.Items {
			if subscriptionStore.Items[i].ID != id {
				continue
			}
			item := &subscriptionStore.Items[i]
			oldURL := item.URL
			item.Name = name
			item.URL = urlValue
			item.Headers = headers
			if oldURL != urlValue {
				item.Content = ""
				item.Status = "未更新"
				item.Error = ""
				item.StatusCode = 0
				item.ContentType = ""
				item.ContentLength = 0
				item.UpdatedAt = ""
			}
			if err := saveSubscriptionStoreLocked(); err != nil {
				return nil, err
			}
			copy := *item
			return &copy, nil
		}
		return nil, fmt.Errorf("未找到要编辑的订阅: %s", id)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	item := subscription{
		ID:        fmt.Sprintf("sub-%d", time.Now().UnixNano()),
		Name:      name,
		URL:       urlValue,
		Headers:   headers,
		Status:    "未更新",
		CreatedAt: now,
	}
	subscriptionStore.Items = append(subscriptionStore.Items, item)
	if err := saveSubscriptionStoreLocked(); err != nil {
		return nil, err
	}
	return &item, nil
}

func updateSubscription(ctx context.Context, id string) (*subscription, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("缺少订阅 ID")
	}
	item, err := getSubscription(id)
	if err != nil {
		return nil, err
	}

	body, statusCode, contentType, fetchErr := fetchSubscription(ctx, *item)
	now := time.Now().UTC().Format(time.RFC3339)

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != id {
			continue
		}
		current := &subscriptionStore.Items[i]
		current.UpdatedAt = now
		current.StatusCode = statusCode
		current.ContentType = contentType
		if fetchErr != nil {
			current.Status = "更新失败"
			current.Error = fetchErr.Error()
			if current.Content == "" {
				current.ContentLength = 0
			}
		} else {
			current.Status = "已更新"
			current.Error = ""
			current.Content = string(body)
			current.ContentLength = int64(len(body))
		}
		if saveErr := saveSubscriptionStoreLocked(); saveErr != nil {
			return nil, saveErr
		}
		copy := *current
		if fetchErr != nil {
			return &copy, fmt.Errorf("%w", fetchErr)
		}
		return &copy, nil
	}
	return nil, fmt.Errorf("未找到订阅: %s", id)
}

func deleteSubscription(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("缺少订阅 ID")
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != id {
			continue
		}
		subscriptionStore.Items = append(subscriptionStore.Items[:i], subscriptionStore.Items[i+1:]...)
		return saveSubscriptionStoreLocked()
	}
	return fmt.Errorf("未找到订阅: %s", id)
}
