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
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	subscriptionStoreFile = "subscriptions.json"
	subscriptionMaxBytes  = 32 << 20 // 32 MiB, measured after optional gzip decompression.
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

func subscriptionDataDirFromArgs() string {
	args := os.Args
	for i := 1; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "-data-dir" || arg == "--data-dir" {
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
			continue
		}
		for _, prefix := range []string{"-data-dir=", "--data-dir="} {
			if strings.HasPrefix(arg, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(arg, prefix))
			}
		}
	}
	return ""
}

func subscriptionDataDir() (string, error) {
	// Android passes the writable app-private directory explicitly. This is the
	// authoritative path for runtime subscription data.
	if dir := subscriptionDataDirFromArgs(); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(os.Getenv("CFDATA_DATA_DIR")); dir != "" {
		return dir, nil
	}

	if runtime.GOOS == "android" {
		return "", fmt.Errorf("Android 未提供可写数据目录，请使用 -data-dir <app files dir>")
	}

	// Desktop/server compatibility: use the current working directory first.
	if workingDir, err := os.Getwd(); err == nil && strings.TrimSpace(workingDir) != "" {
		return workingDir, nil
	}

	// Legacy fallback for non-Android direct launches.
	return filepath.Dir(os.Args[0]), nil
}

func subscriptionFilePath() (string, error) {
	dir, err := subscriptionDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, subscriptionStoreFile), nil
}

func ensureSubscriptionStoreLoaded() error {
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	if subscriptionStore.Loaded {
		return nil
	}

	path, err := subscriptionFilePath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
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

	if items == nil {
		items = []subscription{}
	}
	for i := range items {
		items[i].Name = strings.TrimSpace(items[i].Name)
		items[i].URL = strings.TrimSpace(items[i].URL)
		items[i].Headers = normalizeSubscriptionHeaders(items[i].Headers)
		if items[i].Status == "" {
			items[i].Status = "未更新"
		}
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

	// Reuse the project's atomic file writer when available.  It writes the
	// complete JSON to a temporary file and replaces the destination safely.
	path, err := subscriptionFilePath()
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0644)
}

func subscriptionSummaryOf(item subscription) subscriptionSummary {
	return subscriptionSummary{
		ID:            item.ID,
		Name:          item.Name,
		URL:           item.URL,
		Headers:       cloneSubscriptionHeaders(item.Headers),
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

func cloneSubscriptionHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		result[k] = v
	}
	return result
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
	id = strings.TrimSpace(id)
	if id == "" {
		return subscription{}, fmt.Errorf("缺少订阅 ID")
	}

	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscription{}, err
	}

	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()

	for _, item := range subscriptionStore.Items {
		if item.ID == id {
			item.Headers = cloneSubscriptionHeaders(item.Headers)
			return item, nil
		}
	}

	return subscription{}, fmt.Errorf("订阅不存在: %s", id)
}

func normalizeSubscriptionHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	result := make(map[string]string, len(headers))
	for k, v := range headers {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		result[k] = v
	}
	if len(result) == 0 {
		return nil
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

	for k, v := range normalizeSubscriptionHeaders(target.Headers) {
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
	urlText := strings.TrimSpace(req.URL)
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
			URL:       urlText,
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

	id := strings.TrimSpace(req.ID)
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != id {
			continue
		}

		item := &subscriptionStore.Items[i]
		urlChanged := item.URL != urlText
		item.Name = name
		item.URL = urlText
		item.Headers = headers
		if urlChanged {
			item.Status = "未更新"
			item.Error = ""
			item.StatusCode = 0
			item.ContentType = ""
			item.ContentLength = 0
			item.UpdatedAt = ""
			item.Format = ""
			item.Content = ""
		}

		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(*item), nil
	}

	return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", id)
}

func updateSubscription(ctx context.Context, id string) (subscriptionSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return subscriptionSummary{}, fmt.Errorf("缺少订阅 ID")
	}

	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscriptionSummary{}, err
	}

	item, err := getSubscription(id)
	if err != nil {
		return subscriptionSummary{}, err
	}

	// Mark the item busy before doing network I/O, but do not hold the store
	// mutex while waiting on the remote subscription server.
	subscriptionStore.Lock()
	found := false
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID == id {
			subscriptionStore.Items[i].Status = "更新中"
			subscriptionStore.Items[i].Error = ""
			found = true
			break
		}
	}
	if !found {
		subscriptionStore.Unlock()
		return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", id)
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

		// Keep the record's creation metadata and identity controlled by the
		// stored entry; only replace fetched/remote-related fields.
		updated.ID = subscriptionStore.Items[i].ID
		updated.CreatedAt = subscriptionStore.Items[i].CreatedAt
		updated.Name = subscriptionStore.Items[i].Name
		updated.URL = subscriptionStore.Items[i].URL
		updated.Headers = cloneSubscriptionHeaders(subscriptionStore.Items[i].Headers)
		subscriptionStore.Items[i] = updated

		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(subscriptionStore.Items[i]), nil
	}

	return subscriptionSummary{}, fmt.Errorf("更新完成后订阅不存在: %s", id)
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

		subscriptionStore.Items = append(
			subscriptionStore.Items[:i],
			subscriptionStore.Items[i+1:]...,
		)
		return saveSubscriptionStoreLocked()
	}

	return fmt.Errorf("订阅不存在: %s", id)
}
