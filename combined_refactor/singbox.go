package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	singBoxSubscriptionRootDefault       = "singbox_subscriptions"
	singBoxConfigTemplateDefault         = "singbox-r-template.json"
	singBoxProviderFileDefault           = "Provider1.json"
	singBoxEngineConfigFile              = "singbox-engine.json"
	singBoxDefaultRepeat                 = 3
	singBoxDefaultTimeout                = 8 * time.Second
	singBoxDefaultProviderInterval       = "24h"
	singBoxDefaultDownloadBytes    int64 = 10 << 20
	singBoxMaxRepeat                     = 10
	singBoxMaxBatchNodes                 = 3000
)

type singBoxConfiguredSubscription struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type singBoxEngineConfig struct {
	Version                int                    `json:"version"`
	SubscriptionRoot       string                 `json:"subscription_root"`
	ConfigTemplate         string                 `json:"config_template"`
	ProviderFile           string                 `json:"provider_file"`
	ProviderUpdateInterval string                 `json:"provider_update_interval"`
	ProviderExclude        string                 `json:"provider_exclude"`
	ProviderHealthCheck    map[string]interface{} `json:"provider_health_check"`
	StartupSync            bool                   `json:"startup_sync"`
	SyncTimeoutSeconds     int                    `json:"sync_timeout_seconds"`
	TestRepeat             int                    `json:"test_repeat"`
	TestTimeoutSeconds     int                    `json:"test_timeout_seconds"`
	DownloadTestBytes      int64                  `json:"download_test_bytes"`
	DownloadTestURL        string                 `json:"download_test_url"`
}

type singBoxNodeSource struct {
	SubscriptionID   string `json:"subscriptionId"`
	SubscriptionName string `json:"subscriptionName"`
	SubscriptionURL  string `json:"subscriptionUrl,omitempty"`
	ProviderTag      string `json:"providerTag,omitempty"`
	NodeTag          string `json:"nodeTag,omitempty"`
	Raw              string `json:"raw,omitempty"`
}

type singBoxNodeVariant struct {
	Protocol    string                 `json:"protocol"`
	Name        string                 `json:"name"`
	Server      string                 `json:"server"`
	Port        int                    `json:"port"`
	Provider    string                 `json:"provider"`
	OutboundTag string                 `json:"outboundTag"`
	Outbound    map[string]interface{} `json:"outbound"`
	Sources     []singBoxNodeSource    `json:"sources"`
}

type singBoxCachedNode struct {
	ID          string                 `json:"id"`
	Server      string                 `json:"server"`
	Port        int                    `json:"port"`
	Protocol    string                 `json:"protocol"`
	Name        string                 `json:"name"`
	Provider    string                 `json:"provider"`
	OutboundTag string                 `json:"outboundTag"`
	Outbound    map[string]interface{} `json:"outbound"`
	Sources     []singBoxNodeSource    `json:"sources"`
	Variants    []singBoxNodeVariant   `json:"variants,omitempty"`
}

type singBoxNodeView struct {
	ID           string                 `json:"id"`
	Server       string                 `json:"server"`
	Port         int                    `json:"port"`
	Protocol     string                 `json:"protocol"`
	Name         string                 `json:"name"`
	Provider     string                 `json:"provider"`
	OutboundTag  string                 `json:"outboundTag"`
	Sources      []singBoxNodeSource    `json:"sources"`
	SourceCount  int                    `json:"sourceCount"`
	VariantCount int                    `json:"variantCount"`
	Outbound     map[string]interface{} `json:"outbound,omitempty"`
	Variants     []singBoxNodeVariant   `json:"variants,omitempty"`
	LastTest     *singBoxNodeResult     `json:"lastTest,omitempty"`
}

type singBoxNodeResult struct {
	NodeID        string               `json:"nodeId"`
	Node          string               `json:"node"`
	Protocol      string               `json:"protocol"`
	Server        string               `json:"server"`
	Port          int                  `json:"port"`
	Success       bool                 `json:"success"`
	SuccessCount  int                  `json:"successCount"`
	TotalAttempts int                  `json:"totalAttempts"`
	LossRate      float64              `json:"lossRate"`
	MinLatencyMS  int64                `json:"minLatencyMs"`
	MaxLatencyMS  int64                `json:"maxLatencyMs"`
	AvgLatencyMS  float64              `json:"avgLatencyMs"`
	Speed         string               `json:"speed,omitempty"`
	OutboundIP    string               `json:"outboundIP,omitempty"`
	WorkingSource string               `json:"workingSource,omitempty"`
	Mode          string               `json:"mode"`
	Results       []singBoxProbeResult `json:"results"`
	Error         string               `json:"error,omitempty"`
}

type singBoxProbeResult struct {
	Attempt    int    `json:"attempt"`
	Success    bool   `json:"success"`
	LatencyMS  int64  `json:"latencyMs"`
	StatusCode int    `json:"statusCode,omitempty"`
	OutboundIP string `json:"outboundIP,omitempty"`
	Error      string `json:"error,omitempty"`
}

type singBoxSyncResponse struct {
	Success       bool     `json:"success"`
	Subscriptions int      `json:"subscriptions"`
	Prepared      int      `json:"prepared"`
	Cached        int      `json:"cached"`
	Nodes         int      `json:"nodes"`
	Errors        []string `json:"errors,omitempty"`
	Error         string   `json:"error,omitempty"`
}

var singBoxState = struct {
	sync.RWMutex
	Results map[string]singBoxNodeResult
}{Results: map[string]singBoxNodeResult{}}

var singBoxSyncMu sync.Mutex

func init() {
	http.HandleFunc("/api/subscription/singbox/nodes", requireAuth(handleSingBoxNodes))
	http.HandleFunc("/api/subscription/singbox/sync-all", requireAuth(handleSingBoxSyncAll))
	http.HandleFunc("/api/subscription/singbox/subscriptions", requireAuth(handleSingBoxSubscriptionsAPI))
	http.HandleFunc("/api/subscription/singbox/diagnostics", requireAuth(handleSingBoxDiagnosticsAPI))
	http.HandleFunc("/api/subscription/singbox/configs", requireAuth(handleSingBoxConfigsAPI))
	http.HandleFunc("/api/subscription/singbox/sync-state", requireAuth(handleSingBoxSyncStateAPI))
	http.HandleFunc("/api/subscription/singbox/save", requireAuth(handleSingBoxSaveAPI))
	http.HandleFunc("/api/subscription/singbox/update", requireAuth(handleSingBoxUpdateAPI))
	http.HandleFunc("/api/subscription/singbox/delete", requireAuth(handleSingBoxDeleteAPI))
}

func handleSingBoxSubscriptionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	items, err := listSubscriptions()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{"success": true, "subscriptions": items})
}

func handleSingBoxDiagnosticsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	item, err := getSubscription(id)
	if err != nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	configPath, _ := singBoxSubscriptionConfigPath(cfg, item.Name)
	providerPath, _ := singBoxProviderFilePath(cfg, item.Name)
	logDir, _ := singBoxSubscriptionDir(cfg, item.Name)
	logPath := filepath.Join(logDir, "Provider1.update.log")
	result := map[string]interface{}{
		"success":        true,
		"id":             item.ID,
		"name":           item.Name,
		"configPath":     configPath,
		"providerPath":   providerPath,
		"logPath":        logPath,
		"configExists":   false,
		"providerExists": false,
		"providerBytes":  int64(0),
		"nodeCount":      0,
		"providerValid":  false,
	}
	if st, e := os.Stat(configPath); e == nil {
		result["configExists"] = true
		result["configBytes"] = st.Size()
	}
	if st, e := os.Stat(providerPath); e == nil {
		result["providerExists"] = true
		result["providerBytes"] = st.Size()
		result["providerValid"] = providerCacheLooksValid(providerPath)
		nodes, e := loadProviderCacheNodes(providerPath, item, singBoxProviderTag(item.ID))
		if e == nil {
			result["nodeCount"] = len(nodes)
		} else {
			result["providerParseError"] = e.Error()
		}
	}
	if raw, e := os.ReadFile(logPath); e == nil {
		result["logTail"] = lastLogLines(string(raw), 120)
	} else {
		result["logTail"] = "（暂无 Provider1.update.log）"
	}
	writeSingBoxJSON(w, http.StatusOK, result)
}

func handleSingBoxConfigsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items, err := listSubscriptions()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	configs := make([]map[string]interface{}, 0, len(items))
	for _, summary := range items {
		item, e := getSubscription(summary.ID)
		if e != nil {
			continue
		}
		prepared, e := prepareSingBoxSubscriptionConfig(item, cfg)
		if e != nil {
			configs = append(configs, map[string]interface{}{
				"id": item.ID, "name": item.Name, "success": false, "error": e.Error(),
			})
			continue
		}
		raw, e := os.ReadFile(prepared.ConfigPath)
		if e != nil {
			configs = append(configs, map[string]interface{}{
				"id": item.ID, "name": item.Name, "success": false, "error": e.Error(),
			})
			continue
		}
		configs = append(configs, map[string]interface{}{
			"id":           item.ID,
			"name":         item.Name,
			"config":       string(raw),
			"configPath":   prepared.ConfigPath,
			"providerPath": prepared.ProviderPath,
			"success":      true,
		})
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"configs": configs,
	})
}

func handleSingBoxSyncStateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req struct {
		ID        string `json:"id"`
		Success   bool   `json:"success"`
		NodeCount int    `json:"nodeCount"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, err := getSubscription(req.ID)
	if err != nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	configPath, _ := singBoxSubscriptionConfigPath(cfg, item.Name)
	providerPath, _ := singBoxProviderFilePath(cfg, item.Name)
	status := "success"
	message := fmt.Sprintf("Provider1 已由 Android Libbox 同步，节点 %d", req.NodeCount)
	if !req.Success {
		status = "error"
		message = req.Error
	}
	summary, err := updateSubscriptionState(item, status, message, configPath, providerPath)
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{"success": true, "subscription": summary})
}

func handleSingBoxSaveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var body struct {
		subscriptionSaveRequest
		Sync bool `json:"sync"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, err := createOrUpdateSubscription(body.subscriptionSaveRequest)
	if err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err == nil {
		item, err = prepareSingBoxSubscriptionConfig(item, cfg)
	}
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"subscription":     item,
		"requiresCoreSync": true,
		"requestedSync":    body.Sync,
	})
}

func handleSingBoxUpdateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req subscriptionIDRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, err := getSubscription(req.ID)
	if err != nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	item, err = prepareSingBoxSubscriptionConfig(item, cfg)
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "syncError": err.Error(), "subscription": item})
		return
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"subscription":     item,
		"requiresCoreSync": true,
	})
}

func handleSingBoxDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req subscriptionIDRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, err := getSubscription(req.ID)
	if err != nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	cfg, _ := loadSingBoxEngineConfig()
	if err := deleteSubscription(req.ID); err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if dir, e := singBoxSubscriptionDir(cfg, item.Name); e == nil {
		_ = os.RemoveAll(dir)
	}
	if path, e := singBoxSubscriptionConfigPath(cfg, item.Name); e == nil {
		_ = os.Remove(path)
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func dataDirectory() string {
	if value := strings.TrimSpace(os.Getenv("CFDATA_DATA_DIR")); value != "" {
		return value
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return cwd
	}
	return os.TempDir()
}

func singBoxDataDir() string { return dataDirectory() }

func singBoxEngineConfigPath() string {
	return filepath.Join(singBoxDataDir(), singBoxEngineConfigFile)
}

func defaultSingBoxEngineConfig() singBoxEngineConfig {
	return singBoxEngineConfig{
		Version:                8,
		SubscriptionRoot:       singBoxSubscriptionRootDefault,
		ConfigTemplate:         singBoxConfigTemplateDefault,
		ProviderFile:           singBoxProviderFileDefault,
		ProviderUpdateInterval: singBoxDefaultProviderInterval,
		ProviderExclude:        "节点|剩余|套餐|客服|官网",
		ProviderHealthCheck:    map[string]interface{}{"enabled": false},
		StartupSync:            false,
		SyncTimeoutSeconds:     90,
		TestRepeat:             singBoxDefaultRepeat,
		TestTimeoutSeconds:     int(singBoxDefaultTimeout / time.Second),
		DownloadTestBytes:      singBoxDefaultDownloadBytes,
		DownloadTestURL:        "https://speed.cloudflare.com/__down?bytes=99999999",
	}
}

func loadSingBoxEngineConfig() (singBoxEngineConfig, error) {
	cfg := defaultSingBoxEngineConfig()
	if err := os.MkdirAll(singBoxDataDir(), 0700); err != nil {
		return cfg, fmt.Errorf("创建 sing-box 数据目录失败: %w", err)
	}
	path := singBoxEngineConfigPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := atomicWriteFile(path, data, 0600); err != nil {
			return cfg, fmt.Errorf("创建 %s 失败: %w", singBoxEngineConfigFile, err)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析 %s 失败: %w", singBoxEngineConfigFile, err)
	}
	if strings.TrimSpace(cfg.SubscriptionRoot) == "" {
		cfg.SubscriptionRoot = singBoxSubscriptionRootDefault
	}
	if strings.TrimSpace(cfg.ConfigTemplate) == "" {
		cfg.ConfigTemplate = singBoxConfigTemplateDefault
	}
	if strings.TrimSpace(cfg.ProviderFile) == "" {
		cfg.ProviderFile = singBoxProviderFileDefault
	}
	if strings.TrimSpace(cfg.ProviderUpdateInterval) == "" {
		cfg.ProviderUpdateInterval = singBoxDefaultProviderInterval
	}
	if cfg.SyncTimeoutSeconds <= 0 || cfg.SyncTimeoutSeconds > 600 {
		cfg.SyncTimeoutSeconds = 90
	}
	if cfg.TestRepeat <= 0 || cfg.TestRepeat > singBoxMaxRepeat {
		cfg.TestRepeat = singBoxDefaultRepeat
	}
	if cfg.TestTimeoutSeconds <= 0 || cfg.TestTimeoutSeconds > 60 {
		cfg.TestTimeoutSeconds = int(singBoxDefaultTimeout / time.Second)
	}
	if cfg.DownloadTestBytes < 0 {
		cfg.DownloadTestBytes = 0
	}
	if cfg.DownloadTestBytes == 0 {
		cfg.DownloadTestBytes = singBoxDefaultDownloadBytes
	}
	if strings.TrimSpace(cfg.DownloadTestURL) == "" {
		cfg.DownloadTestURL = "https://speed.cloudflare.com/__down?bytes=99999999"
	}
	return cfg, nil
}

func singBoxNodesDir(cfg singBoxEngineConfig) string {
	return filepath.Join(singBoxDataDir(), cfg.SubscriptionRoot)
}

func singBoxSubscriptionDir(cfg singBoxEngineConfig, name string) (string, error) {
	if err := validateSubscriptionName(name); err != nil {
		return "", err
	}
	return filepath.Join(singBoxNodesDir(cfg), name), nil
}

func singBoxSubscriptionConfigPath(cfg singBoxEngineConfig, name string) (string, error) {
	if err := validateSubscriptionName(name); err != nil {
		return "", err
	}
	return filepath.Join(singBoxNodesDir(cfg), name+".json"), nil
}

func singBoxProviderFilePath(cfg singBoxEngineConfig, name string) (string, error) {
	dir, err := singBoxSubscriptionDir(cfg, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cfg.ProviderFile), nil
}

func singBoxProviderRelativePath(cfg singBoxEngineConfig, name string) (string, error) {
	if err := validateSubscriptionName(name); err != nil {
		return "", err
	}
	return "./" + name + "/" + cfg.ProviderFile, nil
}

func singBoxNodeFileName(subscriptionName string) (string, error) {
	if err := validateSubscriptionName(subscriptionName); err != nil {
		return "", err
	}
	return subscriptionName + ".json", nil
}

func singBoxNodeFilePath(cfg singBoxEngineConfig, subscriptionName string) (string, error) {
	name, err := singBoxNodeFileName(subscriptionName)
	if err != nil {
		return "", err
	}
	return filepath.Join(singBoxNodesDir(cfg), name), nil
}

func prepareSingBoxSubscriptionConfig(item subscription, cfg singBoxEngineConfig) (subscriptionSummary, error) {
	if err := validateSubscriptionName(item.Name); err != nil {
		return subscriptionSummary{}, err
	}
	if err := validateSubscriptionURL(item.URL); err != nil {
		return subscriptionSummary{}, err
	}
	root := singBoxNodesDir(cfg)
	if err := os.MkdirAll(root, 0700); err != nil {
		return subscriptionSummary{}, err
	}
	subDir, err := singBoxSubscriptionDir(cfg, item.Name)
	if err != nil {
		return subscriptionSummary{}, err
	}
	if err := os.MkdirAll(subDir, 0700); err != nil {
		return subscriptionSummary{}, err
	}
	configPath, err := singBoxSubscriptionConfigPath(cfg, item.Name)
	if err != nil {
		return subscriptionSummary{}, err
	}
	providerPath, err := singBoxProviderFilePath(cfg, item.Name)
	if err != nil {
		return subscriptionSummary{}, err
	}
	relProviderPath, err := singBoxProviderRelativePath(cfg, item.Name)
	if err != nil {
		return subscriptionSummary{}, err
	}
	relLogPath := filepath.ToSlash(filepath.Join(item.Name, "Provider1.update.log"))
	bootstrap := map[string]interface{}{
		"log": map[string]interface{}{
			"disabled":  false,
			"level":     "info",
			"output":    relLogPath,
			"timestamp": true,
		},
		"providers": []interface{}{map[string]interface{}{
			"tag":             "Provider1",
			"type":            "remote",
			"url":             strings.TrimSpace(item.URL),
			"exclude":         cfg.ProviderExclude,
			"download_detour": "direct",
			"path":            relProviderPath,
			"update_interval": cfg.ProviderUpdateInterval,
			"health_check":    map[string]interface{}{"enabled": false},
		}},
		"outbounds": []interface{}{
			map[string]interface{}{"tag": "direct", "type": "direct"},
		},
		"route": map[string]interface{}{"final": "direct"},
	}
	data, err := json.MarshalIndent(bootstrap, "", "  ")
	if err != nil {
		return subscriptionSummary{}, err
	}
	if err := atomicWriteFile(configPath, data, 0600); err != nil {
		return subscriptionSummary{}, fmt.Errorf("保存 Provider Core 配置失败: %w", err)
	}
	return updateSubscriptionState(item, "pending", "等待 Android Libbox Core 同步", configPath, providerPath)
}

func handleSingBoxNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]singBoxNodeView, 0, len(nodes))
	for _, node := range nodes {
		view := singBoxNodeView{
			ID: node.ID, Server: node.Server, Port: node.Port, Protocol: node.Protocol,
			Name: node.Name, Provider: node.Provider, OutboundTag: node.OutboundTag,
			Sources: node.Sources, SourceCount: len(node.Sources), VariantCount: maxInt(1, len(node.Variants)),
			Outbound: cloneInterfaceMap(node.Outbound), Variants: append([]singBoxNodeVariant(nil), node.Variants...),
		}
		singBoxState.RLock()
		if result, ok := singBoxState.Results[node.ID]; ok {
			copy := result
			view.LastTest = &copy
		}
		singBoxState.RUnlock()
		views = append(views, view)
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{"success": true, "nodes": views})
}

func handleSingBoxSyncAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "1") || strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")
	ctx, cancel := contextWithFiveMinutes(r.Context())
	defer cancel()
	result, err := syncAllSingBoxSubscriptions(ctx, force)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		writeSingBoxJSON(w, http.StatusInternalServerError, result)
		return
	}
	writeSingBoxJSON(w, http.StatusOK, result)
}

func syncAllSingBoxSubscriptions(ctx context.Context, force bool) (singBoxSyncResponse, error) {
	singBoxSyncMu.Lock()
	defer singBoxSyncMu.Unlock()
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return singBoxSyncResponse{}, err
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return singBoxSyncResponse{}, err
	}
	subscriptionStore.Lock()
	items := append([]subscription(nil), subscriptionStore.Items...)
	subscriptionStore.Unlock()
	resp := singBoxSyncResponse{Success: true, Subscriptions: len(items), Errors: []string{}}
	interval := 24 * time.Hour
	if d, e := time.ParseDuration(strings.TrimSpace(cfg.ProviderUpdateInterval)); e == nil && d > 0 {
		interval = d
	}
	for _, item := range items {
		providerPath, pathErr := singBoxProviderFilePath(cfg, item.Name)
		if pathErr != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", item.Name, pathErr))
			continue
		}
		if !force {
			if st, e := os.Stat(providerPath); e == nil && providerCacheLooksValid(providerPath) && time.Since(st.ModTime()) < interval {
				resp.Cached++
				continue
			}
		}
		if _, e := prepareSingBoxSubscriptionConfig(item, cfg); e != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", item.Name, e))
			continue
		}
		resp.Prepared++
	}
	nodes, e := loadAggregatedSingBoxNodes()
	if e == nil {
		resp.Nodes = len(nodes)
	}
	if len(resp.Errors) > 0 {
		resp.Success = false
	}
	return resp, nil
}

func updateSubscriptionState(item subscription, status, message, configPath, providerPath string) (subscriptionSummary, error) {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscriptionSummary{}, err
	}
	count := 0
	if providerCacheLooksValid(providerPath) {
		if nodes, e := loadProviderCacheNodes(providerPath, item, singBoxProviderTag(item.ID)); e == nil {
			count = len(nodes)
		}
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for i := range subscriptionStore.Items {
		if subscriptionStore.Items[i].ID != item.ID {
			continue
		}
		item2 := &subscriptionStore.Items[i]
		item2.Status = status
		item2.Error = ""
		if status == "error" {
			item2.Error = message
		}
		item2.ConfigPath = configPath
		item2.ProviderPath = providerPath
		item2.NodeCount = count
		item2.UpdatedAt = time.Now().Format(time.RFC3339)
		item2.StatusCode = 0
		item2.ContentType = ""
		item2.ContentLength = 0
		item2.Format = "sing-box-provider-libbox"
		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(*item2), nil
	}
	return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", item.ID)
}

func stripProviderInfoHeader(raw string) string {
	raw = strings.TrimPrefix(raw, "\ufeff")
	if !strings.HasPrefix(raw, "#") {
		return raw
	}
	if index := strings.IndexByte(raw, '\n'); index >= 0 {
		return raw[index+1:]
	}
	return ""
}

func providerCacheLooksValid(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := stripProviderInfoHeader(string(raw))
	if len(strings.TrimSpace(content)) == 0 {
		return false
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
		Proxies   []map[string]interface{} `json:"proxies"`
		Nodes     []map[string]interface{} `json:"nodes"`
	}
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false
	}
	return len(doc.Outbounds) > 0 || len(doc.Proxies) > 0 || len(doc.Nodes) > 0
}

func loadAggregatedSingBoxNodes() ([]singBoxCachedNode, error) {
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return nil, err
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	subMap := make(map[string]subscription, len(subscriptionStore.Items))
	for _, item := range subscriptionStore.Items {
		subMap[item.Name] = item
	}
	subscriptionStore.Unlock()
	entries, err := os.ReadDir(singBoxNodesDir(cfg))
	if os.IsNotExist(err) {
		return []singBoxCachedNode{}, nil
	}
	if err != nil {
		return nil, err
	}
	merged := map[string]*singBoxCachedNode{}
	order := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == filepath.Base(singBoxEngineConfigFile) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		item, ok := subMap[name]
		if !ok {
			continue
		}
		providerPath, _ := singBoxProviderFilePath(cfg, name)
		nodes, e := loadProviderCacheNodes(providerPath, item, singBoxProviderTag(item.ID))
		if e != nil {
			continue
		}
		for _, node := range nodes {
			key := singBoxEndpointKey(node.Server, node.Port)
			if ex := merged[key]; ex != nil {
				ex.Sources = uniqueNodeSources(append(ex.Sources, node.Sources...))
				ex.Variants = appendUniqueNodeVariants(ex.Variants, node.Variants...)
				continue
			}
			copy := node
			copy.ID = singBoxNodeID(node.Server, node.Port)
			copy.Sources = uniqueNodeSources(copy.Sources)
			if len(copy.Variants) == 0 {
				copy.Variants = []singBoxNodeVariant{{Protocol: copy.Protocol, Name: copy.Name, Server: copy.Server, Port: copy.Port, Provider: copy.Provider, OutboundTag: copy.OutboundTag, Outbound: cloneInterfaceMap(copy.Outbound), Sources: copy.Sources}}
			}
			merged[key] = &copy
			order = append(order, key)
		}
	}
	result := make([]singBoxCachedNode, 0, len(order))
	for _, key := range order {
		if node := merged[key]; node != nil {
			node.Sources = uniqueNodeSources(node.Sources)
			result = append(result, *node)
		}
	}
	return result, nil
}

func loadProviderCacheNodes(path string, item subscription, providerTag string) ([]singBoxCachedNode, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := stripProviderInfoHeader(string(raw))
	if len(strings.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("Provider 缓存为空")
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
		Proxies   []map[string]interface{} `json:"proxies"`
		Nodes     []map[string]interface{} `json:"nodes"`
	}
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析 Provider 缓存失败: %w", err)
	}
	all := append([]map[string]interface{}{}, doc.Outbounds...)
	all = append(all, doc.Proxies...)
	all = append(all, doc.Nodes...)
	result := make([]singBoxCachedNode, 0, len(all))
	for index, outbound := range all {
		typeName := strings.ToLower(stringValue(outbound["type"]))
		server := stringValue(outbound["server"])
		port := intFromJSONNumber(outbound["server_port"])
		if server == "" || port <= 0 || typeName == "" {
			continue
		}

		rawTag := stringValue(outbound["tag"])
		if rawTag == "" {
			rawTag = stringValue(outbound["name"])
		}
		if rawTag == "" {
			rawTag = fmt.Sprintf("%d", index)
		}

		// ProviderRemote saves lastOutOpts with the original tag, while the
		// runtime adapter exposes it as ProviderTag/tag. Its parser also rewrites
		// internal detours to ProviderTag/tag. Recreate that runtime namespace
		// here, using a unique namespace per CFData subscription so multiple
		// subscriptions can coexist in the temporary test Core.
		runtimeTag := providerRuntimeOutboundTag(providerTag, rawTag)
		normalized := cloneInterfaceMap(outbound)
		normalizeProviderOutbound(normalized, runtimeTag, providerTag)

		name := rawTag
		if i := strings.LastIndex(rawTag, "/"); i >= 0 && i+1 < len(rawTag) {
			name = rawTag[i+1:]
		}
		source := singBoxNodeSource{SubscriptionID: item.ID, SubscriptionName: item.Name, SubscriptionURL: item.URL, ProviderTag: providerTag, NodeTag: rawTag}
		node := singBoxCachedNode{ID: singBoxNodeID(server, port), Server: server, Port: port, Protocol: typeName, Name: name, Provider: providerTag, OutboundTag: runtimeTag, Outbound: normalized, Sources: []singBoxNodeSource{source}, Variants: []singBoxNodeVariant{{Protocol: typeName, Name: name, Server: server, Port: port, Provider: providerTag, OutboundTag: runtimeTag, Outbound: cloneInterfaceMap(normalized), Sources: []singBoxNodeSource{source}}}}
		result = append(result, node)
	}
	return result, nil
}

func providerRuntimeOutboundTag(providerTag, rawTag string) string {
	rawTag = strings.TrimSpace(rawTag)
	providerTag = strings.TrimSpace(providerTag)
	if rawTag == "" {
		return providerTag
	}
	if providerTag == "" {
		return rawTag
	}
	if strings.HasPrefix(rawTag, providerTag+"/") {
		return rawTag
	}
	if index := strings.IndexByte(rawTag, '/'); index >= 0 && strings.HasPrefix(rawTag[:index], "Provider1") {
		return providerTag + rawTag[index:]
	}
	return providerTag + "/" + rawTag
}

func normalizeProviderOutbound(outbound map[string]interface{}, runtimeTag, runtimeProviderTag string) {
	if outbound == nil {
		return
	}
	runtimeTag = strings.TrimSpace(runtimeTag)
	runtimeProviderTag = strings.TrimSpace(runtimeProviderTag)
	normalizeProviderValue(outbound, runtimeProviderTag)
	outbound["tag"] = runtimeTag
}

func normalizeProviderValue(value interface{}, runtimeProviderTag string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			if key == "tag" {
				continue
			}
			if text, ok := item.(string); ok {
				typed[key] = rewriteProviderReference(text, runtimeProviderTag)
				continue
			}
			typed[key] = normalizeProviderValue(item, runtimeProviderTag)
		}
	case []interface{}:
		for index, item := range typed {
			typed[index] = normalizeProviderValue(item, runtimeProviderTag)
		}
	}
	return value
}

func rewriteProviderReference(value, runtimeProviderTag string) string {
	value = strings.TrimSpace(value)
	runtimeProviderTag = strings.TrimSpace(runtimeProviderTag)
	if runtimeProviderTag == "" || value == "" {
		return value
	}
	if strings.HasPrefix(value, runtimeProviderTag+"/") {
		return value
	}
	if index := strings.IndexByte(value, '/'); index >= 0 && strings.HasPrefix(value[:index], "Provider1") {
		return runtimeProviderTag + value[index:]
	}
	return value
}

func providerRefsForNodes(cfg singBoxEngineConfig, nodes []singBoxCachedNode) []string {
	seen := map[string]bool{}
	refs := make([]string, 0)
	for _, node := range nodes {
		if node.Provider != "" && !seen[node.Provider] {
			seen[node.Provider] = true
			refs = append(refs, node.Provider)
		}
	}
	return refs
}

func providerCacheForTagExists(cfg singBoxEngineConfig, providerTag string) bool {
	item := subscriptionByProviderTag(providerTag)
	if item.ID == "" {
		return false
	}
	path, err := singBoxProviderFilePath(cfg, item.Name)
	if err != nil {
		return false
	}
	return providerCacheLooksValid(path)
}

func subscriptionByProviderTag(providerTag string) subscription {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscription{}
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for _, item := range subscriptionStore.Items {
		if singBoxProviderTag(item.ID) == providerTag {
			return item
		}
	}
	return subscription{}
}

func singBoxProviderTag(subscriptionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(subscriptionID)))
	return "sub-" + hex.EncodeToString(sum[:6])
}

func singBoxEndpointKey(server string, port int) string {
	return strings.ToLower(strings.TrimSpace(server)) + ":" + fmt.Sprintf("%d", port)
}

func singBoxNodeID(server string, port int) string {
	h := sha256.Sum256([]byte(singBoxEndpointKey(server, port)))
	return hex.EncodeToString(h[:8])
}

func cloneInterfaceMap(value map[string]interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]interface{}{}
	}
	var clone map[string]interface{}
	if err := json.Unmarshal(data, &clone); err != nil {
		return map[string]interface{}{}
	}
	return clone
}

func uniqueNodeSources(input []singBoxNodeSource) []singBoxNodeSource {
	seen := make(map[string]bool)
	result := make([]singBoxNodeSource, 0, len(input))
	for _, source := range input {
		key := source.SubscriptionID + "\x00" + source.NodeTag + "\x00" + source.Raw
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, source)
	}
	return result
}

func appendUniqueNodeVariants(base []singBoxNodeVariant, additions ...singBoxNodeVariant) []singBoxNodeVariant {
	seen := make(map[string]bool, len(base)+len(additions))
	result := append([]singBoxNodeVariant(nil), base...)
	for _, item := range base {
		seen[item.Provider+"\x00"+item.OutboundTag+"\x00"+item.Protocol] = true
	}
	for _, item := range additions {
		key := item.Provider + "\x00" + item.OutboundTag + "\x00" + item.Protocol
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}

func firstSourceName(sources []singBoxNodeSource) string {
	if len(sources) == 0 {
		return ""
	}
	if sources[0].SubscriptionName != "" {
		return sources[0].SubscriptionName
	}
	return sources[0].SubscriptionID
}

func firstProbeError(results []singBoxProbeResult) string {
	for _, result := range results {
		if !result.Success && result.Error != "" {
			return result.Error
		}
	}
	return "连接失败"
}

func intFromJSONNumber(value interface{}) int {
	switch number := value.(type) {
	case json.Number:
		if value64, err := number.Int64(); err == nil {
			return int(value64)
		}
	case float64:
		return int(number)
	case float32:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	case string:
		parsed, _ := fmt.Sscanf(strings.TrimSpace(number), "%d", new(int))
		if parsed == 1 {
			var out int
			_, _ = fmt.Sscanf(strings.TrimSpace(number), "%d", &out)
			return out
		}
	}
	return 0
}

func stringValue(value interface{}) string {
	switch text := value.(type) {
	case string:
		return strings.TrimSpace(text)
	case json.Number:
		return text.String()
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func findRawEndpointForSubscription(content, server string, port int) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	if content == "" {
		return ""
	}
	decoded := decodeMaybeBase64Subscription(content)
	needle := server + ":" + fmt.Sprintf("%d", port)
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func lastLogLines(text string, maxLines int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) <= maxLines {
		return strings.TrimSpace(text)
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func decodeMaybeBase64Subscription(content string) string {
	trimmed := strings.TrimSpace(content)
	if decoded, err := base64DecodeURLSafe(trimmed); err == nil && strings.Contains(decoded, "://") {
		return decoded
	}
	if decoded, err := base64DecodeStd(trimmed); err == nil && strings.Contains(decoded, "://") {
		return decoded
	}
	return content
}

func decodeBase64URL(value string) (string, error) {
	return base64DecodeURLSafe(value)
}

func base64DecodeStd(value string) (string, error) {
	clean := strings.TrimSpace(value)
	padding := len(clean) % 4
	if padding != 0 {
		clean += strings.Repeat("=", 4-padding)
	}
	data, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func base64DecodeURLSafe(value string) (string, error) {
	clean := strings.TrimSpace(value)
	clean = strings.ReplaceAll(clean, "-", "+")
	clean = strings.ReplaceAll(clean, "_", "/")
	padding := len(clean) % 4
	if padding != 0 {
		clean += strings.Repeat("=", 4-padding)
	}
	data, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func writeSingBoxJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
