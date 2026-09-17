package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const subscriptionStoreFile = "subscriptions.json"

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
	Content       string            `json:"content,omitempty"` // legacy compatibility only
	Status        string            `json:"status"`
	Error         string            `json:"error,omitempty"`
	StatusCode    int               `json:"statusCode,omitempty"`
	ContentType   string            `json:"contentType,omitempty"`
	ContentLength int64             `json:"contentLength,omitempty"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	Format        string            `json:"format,omitempty"`
	ConfigPath    string            `json:"configPath,omitempty"`
	ProviderPath  string            `json:"providerPath,omitempty"`
	NodeCount     int               `json:"nodeCount,omitempty"`
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
	ConfigPath    string            `json:"configPath,omitempty"`
	ProviderPath  string            `json:"providerPath,omitempty"`
	NodeCount     int               `json:"nodeCount,omitempty"`
}

func subscriptionFilePath() string {
	if dataDir := strings.TrimSpace(os.Getenv("CFDATA_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, subscriptionStoreFile)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Join(cwd, subscriptionStoreFile)
	}
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
	if items == nil {
		items = []subscription{}
	}
	for i := range items {
		items[i].Name = strings.TrimSpace(items[i].Name)
		items[i].URL = strings.TrimSpace(items[i].URL)
		items[i].Headers = normalizeSubscriptionHeaders(items[i].Headers)
		if items[i].Status == "" {
			items[i].Status = "未同步"
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
	return atomicWriteFile(subscriptionFilePath(), data, 0644)
}
func cloneSubscriptionHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	r := make(map[string]string, len(h))
	for k, v := range h {
		r[k] = v
	}
	return r
}
func normalizeSubscriptionHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	r := make(map[string]string, len(h))
	for k, v := range h {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			r[k] = v
		}
	}
	if len(r) == 0 {
		return nil
	}
	return r
}
func subscriptionSummaryOf(item subscription) subscriptionSummary {
	return subscriptionSummary{ID: item.ID, Name: item.Name, URL: item.URL, Headers: cloneSubscriptionHeaders(item.Headers), Status: item.Status, Error: item.Error, StatusCode: item.StatusCode, ContentType: item.ContentType, ContentLength: item.ContentLength, UpdatedAt: item.UpdatedAt, CreatedAt: item.CreatedAt, Format: item.Format, HasContent: strings.TrimSpace(item.Content) != "", ConfigPath: item.ConfigPath, ProviderPath: item.ProviderPath, NodeCount: item.NodeCount}
}
func listSubscriptions() ([]subscriptionSummary, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	r := make([]subscriptionSummary, 0, len(subscriptionStore.Items))
	for _, item := range subscriptionStore.Items {
		r = append(r, subscriptionSummaryOf(item))
	}
	return r, nil
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
func validateSubscriptionName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("订阅名称不能为空")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\:*?"<>|`) {
		return fmt.Errorf("订阅名称包含文件名不允许的字符")
	}
	return nil
}
func validateSubscriptionURL(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := urlParseSafe(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("订阅 URL 必须使用 http 或 https")
	}
	if u.Host == "" {
		return fmt.Errorf("订阅 URL 缺少主机名")
	}
	return nil
}
func urlParseSafe(raw string) (*url.URL, error) { return url.Parse(strings.TrimSpace(raw)) }
func subscriptionNameExistsLocked(name, exceptID string) bool {
	for _, item := range subscriptionStore.Items {
		if item.ID == exceptID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}
func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func createOrUpdateSubscription(req subscriptionSaveRequest) (subscriptionSummary, error) {
	if err := validateSubscriptionURL(req.URL); err != nil {
		return subscriptionSummary{}, err
	}
	name := strings.TrimSpace(req.Name)
	if err := validateSubscriptionName(name); err != nil {
		return subscriptionSummary{}, err
	}
	urlText := strings.TrimSpace(req.URL)
	headers := normalizeSubscriptionHeaders(req.Headers)
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscriptionSummary{}, err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	exceptID := strings.TrimSpace(req.ID)
	if subscriptionNameExistsLocked(name, exceptID) {
		return subscriptionSummary{}, fmt.Errorf("订阅名称已存在: %s", name)
	}
	now := time.Now().Format(time.RFC3339)
	if exceptID == "" {
		item := subscription{ID: fmt.Sprintf("sub-%d", time.Now().UnixNano()), Name: name, URL: urlText, Headers: headers, Status: "未同步", CreatedAt: now}
		subscriptionStore.Items = append(subscriptionStore.Items, item)
		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(item), nil
	}
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != exceptID {
			continue
		}
		item := &subscriptionStore.Items[i]
		changed := item.URL != urlText || !sameStringMap(item.Headers, headers) || !strings.EqualFold(item.Name, name)
		item.Name = name
		item.URL = urlText
		item.Headers = headers
		if changed {
			item.Status = "未同步"
			item.Error = ""
			item.StatusCode = 0
			item.ContentType = ""
			item.ContentLength = 0
			item.UpdatedAt = ""
			item.Format = ""
			item.Content = ""
			item.ConfigPath = ""
			item.ProviderPath = ""
			item.NodeCount = 0
		}
		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(*item), nil
	}
	return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", exceptID)
}
func updateSubscription(ctx context.Context, id string) (subscriptionSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return subscriptionSummary{}, fmt.Errorf("缺少订阅 ID")
	}
	item, err := getSubscription(id)
	if err != nil {
		return subscriptionSummary{}, err
	}
	singBoxSyncMu.Lock()
	defer singBoxSyncMu.Unlock()
	return syncOneSingBoxSubscription(ctx, item, true)
}
func deleteSubscription(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("缺少订阅 ID")
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return err
	}
	removedName := ""
	subscriptionStore.Lock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != id {
			continue
		}
		removedName = subscriptionStore.Items[i].Name
		subscriptionStore.Items = append(subscriptionStore.Items[:i], subscriptionStore.Items[i+1:]...)
		err := saveSubscriptionStoreLocked()
		subscriptionStore.Unlock()
		if err != nil {
			return err
		}
		if cfg, cfgErr := loadSingBoxEngineConfig(); cfgErr == nil {
			if dir, dirErr := singBoxSubscriptionDir(cfg, removedName); dirErr == nil {
				_ = os.RemoveAll(dir)
			}
			if path, pathErr := singBoxSubscriptionConfigPath(cfg, removedName); pathErr == nil {
				_ = os.Remove(path)
			}
		}
		return nil
	}
	subscriptionStore.Unlock()
	return fmt.Errorf("订阅不存在: %s", id)
}
