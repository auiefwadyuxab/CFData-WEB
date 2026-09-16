package main

import (
	"compress/gzip"
	"context"
	"encoding/base64"
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
	subscriptionMaxBytes  = 32 << 20 // 32 MiB
)

var subscriptionStore struct {
	sync.Mutex
	Loaded bool
	Items  []subscription
}

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
	Format        string            `json:"format,omitempty"`
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
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	Status        string            `json:"status"`
	Error         string            `json:"error,omitempty"`
	StatusCode    int               `json:"statusCode,omitempty"`
	ContentType   string            `json:"contentType,omitempty"`
	ContentLength int64             `json:"contentLength,omitempty"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	Format        string            `json:"format,omitempty"`
	HasContent    bool              `json:"hasContent"`
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

	raw, err := os.ReadFile(subscriptionFilePath())
	if os.IsNotExist(err) {
		subscriptionStore.Items = []subscription{}
		subscriptionStore.Loaded = true
		return nil
	}
	if err != nil {
		return err
	}

	if len(strings.TrimSpace(string(raw))) == 0 {
		subscriptionStore.Items = []subscription{}
		subscriptionStore.Loaded = true
		return nil
	}

	var items []subscription
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", subscriptionStoreFile, err)
	}

	subscriptionStore.Items = items
	subscriptionStore.Loaded = true
	return nil
}

func saveSubscriptionStoreLocked() error {
	data, err := json.MarshalIndent(subscriptionStore.Items, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(subscriptionFilePath(), data, 0644)
}

func subscriptionSummaryOf(item subscription) subscriptionSummary {
	return subscriptionSummary{
		ID:            item.ID,
		Name:          item.Name,
		URL:           item.URL,
		Headers:       item.Headers,
		Status:        item.Status,
		Error:         item.Error,
		StatusCode:    item.StatusCode,
		ContentType:   item.ContentType,
		ContentLength: item.ContentLength,
		UpdatedAt:     item.UpdatedAt,
		CreatedAt:     item.CreatedAt,
		Format:        item.Format,
		HasContent:    strings.TrimSpace(item.Content) != "",
	}
}

func listSubscriptions() ([]subscriptionSummary, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	result := make([]subscriptionSummary, 0, len(subscriptionStore.Items))
	for _, item := range subscriptionStore.Items {
		result = append(result, subscriptionSummaryOf(item))
	}
	return result, nil
}

func getSubscription(id string) (subscription, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscription{}, err
	}

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	for _, item := range subscriptionStore.Items {
		if item.ID == id {
			return item, nil
		}
	}

	return subscription{}, fmt.Errorf("订阅不存在: %s", id)
}

func normalizeSubscriptionHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string)

	for k, v := range headers {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		result[k] = v
	}

	return result
}

func validateSubscriptionURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("订阅 URL 无效: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("订阅 URL 必须使用 http 或 https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("订阅 URL 缺少主机名")
	}

	return nil
}

func detectSubscriptionFormat(content string) string {
	text := strings.TrimSpace(strings.TrimPrefix(content, "\uFEFF"))
	if text == "" {
		return "empty"
	}

	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		var parsed interface{}
		if json.Unmarshal([]byte(text), &parsed) == nil {
			return "json"
		}
	}

	if strings.Contains(text, "://") {
		return "uri"
	}

	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, text)

	if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil {
		decodedText := strings.TrimSpace(string(decoded))
		if strings.Contains(decodedText, "://") {
			return "base64-uri"
		}
	}

	return "text"
}

func fetchSubscription(ctx context.Context, target subscription) (subscription, error) {
	if err := validateSubscriptionURL(target.URL); err != nil {
		return target, err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimSpace(target.URL),
		nil,
	)
	if err != nil {
		return target, err
	}

	headers := normalizeSubscriptionHeaders(target.Headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := upstreamHTTPClient.Do(req)
	if err != nil {
		return target, err
	}
	defer resp.Body.Close()

	target.StatusCode = resp.StatusCode
	target.ContentType = resp.Header.Get("Content-Type")

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			return target, fmt.Errorf("订阅请求失败: %s: %s", resp.Status, message)
		}
		return target, fmt.Errorf("订阅请求失败: %s", resp.Status)
	}

	var reader io.Reader = resp.Body

	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return target, fmt.Errorf("gzip 解压失败: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	data, err := io.ReadAll(io.LimitReader(reader, subscriptionMaxBytes+1))
	if err != nil {
		return target, err
	}

	if len(data) > subscriptionMaxBytes {
		return target, fmt.Errorf("订阅内容超过 %d MiB，已拒绝保存", subscriptionMaxBytes>>20)
	}

	target.Content = string(data)
	target.ContentLength = int64(len(data))
	target.UpdatedAt = time.Now().Format(time.RFC3339)
	target.Status = "success"
	target.Error = ""
	target.Format = detectSubscriptionFormat(target.Content)

	return target, nil
}

func createOrUpdateSubscription(req subscriptionSaveRequest) (subscriptionSummary, error) {
	if err := validateSubscriptionURL(req.URL); err != nil {
		return subscriptionSummary{}, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "未命名订阅"
	}

	headers := normalizeSubscriptionHeaders(req.Headers)

	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscriptionSummary{}, err
	}

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	now := time.Now().Format(time.RFC3339)

	if strings.TrimSpace(req.ID) == "" {
		item := subscription{
			ID:        fmt.Sprintf("sub-%d", time.Now().UnixNano()),
			Name:      name,
			URL:       strings.TrimSpace(req.URL),
			Headers:   headers,
			Status:    "未更新",
			CreatedAt: now,
		}

		subscriptionStore.Items = append(subscriptionStore.Items, item)

		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}

		return subscriptionSummaryOf(item), nil
	}

	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != req.ID {
			continue
		}

		item := &subscriptionStore.Items[i]
		item.Name = name
		item.URL = strings.TrimSpace(req.URL)
		item.Headers = headers

		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}

		return subscriptionSummaryOf(*item), nil
	}

	return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", req.ID)
}

func updateSubscription(ctx context.Context, id string) (subscriptionSummary, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscriptionSummary{}, err
	}

	item, err := getSubscription(id)
	if err != nil {
		return subscriptionSummary{}, err
	}

	item.Status = "更新中"
	item.Error = ""

	subscriptionStore.Lock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID == id {
			subscriptionStore.Items[i].Status = "更新中"
			subscriptionStore.Items[i].Error = ""
			break
		}
	}
	if err := saveSubscriptionStoreLocked(); err != nil {
		subscriptionStore.Unlock()
		return subscriptionSummary{}, err
	}
	subscriptionStore.Unlock()

	updated, fetchErr := fetchSubscription(ctx, item)

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != id {
			continue
		}

		if fetchErr != nil {
			subscriptionStore.Items[i].Status = "error"
			subscriptionStore.Items[i].Error = fetchErr.Error()

			if err := saveSubscriptionStoreLocked(); err != nil {
				return subscriptionSummary{}, err
			}
			return subscriptionSummaryOf(subscriptionStore.Items[i]), fetchErr
		}

		subscriptionStore.Items[i] = updated

		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}

		return subscriptionSummaryOf(subscriptionStore.Items[i]), nil
	}

	return subscriptionSummary{}, fmt.Errorf("更新完成后订阅不存在: %s", id)
}

func deleteSubscription(id string) error {
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

	return fmt.Errorf("订阅不存在: %s", id)
}