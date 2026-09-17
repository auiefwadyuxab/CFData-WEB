package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	singBoxDefaultUpdateInterval         = 6 * time.Hour
	singBoxDefaultProviderInterval       = "24h"
	singBoxDefaultDownloadBytes    int64 = 10 << 20
	singBoxMaxRepeat                     = 10
	singBoxMaxBatchNodes                 = 3000
	singBoxSelectorTag                   = "cfdata-select"
	singBoxTunTag                        = "cfdata-tun"
	singBoxMixedTag                      = "cfdata-mixed"
	singBoxControllerSecretBytes         = 18
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
	TestTransport          string                 `json:"test_transport"`
	PreferSingTun          bool                   `json:"prefer_sing_tun"`
	SingTunRequiresRoot    bool                   `json:"sing_tun_requires_root"`
	MixedFallback          bool                   `json:"mixed_fallback"`
	LocalProxyHost         string                 `json:"local_proxy_host"`
	ControllerHost         string                 `json:"controller_host"`
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
	ID           string              `json:"id"`
	Server       string              `json:"server"`
	Port         int                 `json:"port"`
	Protocol     string              `json:"protocol"`
	Name         string              `json:"name"`
	Provider     string              `json:"provider"`
	OutboundTag  string              `json:"outboundTag"`
	Sources      []singBoxNodeSource `json:"sources"`
	SourceCount  int                 `json:"sourceCount"`
	VariantCount int                 `json:"variantCount"`
	LastTest     *singBoxNodeResult  `json:"lastTest,omitempty"`
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
	Updated       int      `json:"updated"`
	Cached        int      `json:"cached"`
	Nodes         int      `json:"nodes"`
	Errors        []string `json:"errors,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type singBoxTestRequest struct {
	NodeID         string `json:"nodeId"`
	Repeat         int    `json:"repeat"`
	TimeoutSeconds int    `json:"timeout"`
	DownloadBytes  int64  `json:"downloadBytes"`
	DownloadURL    string `json:"downloadUrl"`
	VariantIndex   int    `json:"variantIndex"`
}

type singBoxBatchTestRequest struct {
	NodeIDs        []string `json:"nodeIds"`
	Repeat         int      `json:"repeat"`
	TimeoutSeconds int      `json:"timeout"`
	DownloadBytes  int64    `json:"downloadBytes"`
	DownloadURL    string   `json:"downloadUrl"`
}

type singBoxBatchTestResponse struct {
	Success bool                `json:"success"`
	Total   int                 `json:"total"`
	Passed  int                 `json:"passed"`
	Results []singBoxNodeResult `json:"results"`
	Error   string              `json:"error,omitempty"`
}

type singBoxRuntime struct {
	process       *exec.Cmd
	configPath    string
	logPath       string
	mode          string
	controllerURL string
	secret        string
	proxyURL      string
	closeOnce     sync.Once
}

var singBoxState = struct {
	sync.RWMutex
	Results map[string]singBoxNodeResult
}{Results: map[string]singBoxNodeResult{}}

var singBoxSyncMu sync.Mutex

func init() {
	http.HandleFunc("/api/subscription/singbox/nodes", requireAuth(handleSingBoxNodes))
	http.HandleFunc("/api/subscription/singbox/sync-all", requireAuth(handleSingBoxSyncAll))
	http.HandleFunc("/api/subscription/singbox/test", requireAuth(handleSingBoxTest))
	http.HandleFunc("/api/subscription/singbox/test-batch", requireAuth(handleSingBoxBatchTest))
	http.HandleFunc("/api/subscription/singbox/subscriptions", requireAuth(handleSingBoxSubscriptionsAPI))
	http.HandleFunc("/api/subscription/singbox/save", requireAuth(handleSingBoxSaveAPI))
	http.HandleFunc("/api/subscription/singbox/update", requireAuth(handleSingBoxUpdateAPI))
	http.HandleFunc("/api/subscription/singbox/delete", requireAuth(handleSingBoxDeleteAPI))
}

func handleSingBoxSubscriptionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, 405, map[string]string{"error": "Method Not Allowed"})
		return
	}
	items, e := listSubscriptions()
	if e != nil {
		writeSingBoxJSON(w, 500, map[string]string{"error": e.Error()})
		return
	}
	writeSingBoxJSON(w, 200, map[string]interface{}{"success": true, "subscriptions": items})
}
func handleSingBoxSaveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, 405, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var body struct {
		subscriptionSaveRequest
		Sync bool `json:"sync"`
	}
	if e := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); e != nil {
		writeSingBoxJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	item, e := createOrUpdateSubscription(body.subscriptionSaveRequest)
	if e != nil {
		writeSingBoxJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	summary := item
	if body.Sync {
		stored, e := getSubscription(item.ID)
		if e != nil {
			writeSingBoxJSON(w, 500, map[string]string{"error": e.Error()})
			return
		}
		singBoxSyncMu.Lock()
		summary, e = syncOneSingBoxSubscription(r.Context(), stored, true)
		singBoxSyncMu.Unlock()
		if e != nil {
			writeSingBoxJSON(w, 200, map[string]interface{}{"success": true, "syncError": e.Error(), "subscription": summary})
			return
		}
	}
	writeSingBoxJSON(w, 200, map[string]interface{}{"success": true, "subscription": summary})
}

func handleSingBoxUpdateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, 405, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req subscriptionIDRequest
	if e := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); e != nil {
		writeSingBoxJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	item, e := getSubscription(req.ID)
	if e != nil {
		writeSingBoxJSON(w, 404, map[string]string{"error": e.Error()})
		return
	}
	summary, e := syncOneSingBoxSubscription(r.Context(), item, true)
	if e != nil {
		writeSingBoxJSON(w, 500, map[string]interface{}{"success": false, "syncError": e.Error(), "subscription": summary})
		return
	}
	writeSingBoxJSON(w, 200, map[string]interface{}{"success": true, "subscription": summary})
}
func handleSingBoxDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, 405, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req subscriptionIDRequest
	if e := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); e != nil {
		writeSingBoxJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	item, e := getSubscription(req.ID)
	if e != nil {
		writeSingBoxJSON(w, 404, map[string]string{"error": e.Error()})
		return
	}
	cfg, _ := loadSingBoxEngineConfig()
	_ = deleteSubscription(req.ID)
	if dir, e := singBoxSubscriptionDir(cfg, item.Name); e == nil {
		_ = os.RemoveAll(dir)
	}
	if path, e := singBoxSubscriptionConfigPath(cfg, item.Name); e == nil {
		_ = os.Remove(path)
	}
	writeSingBoxJSON(w, 200, map[string]interface{}{"success": true})
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
		Version: 5, SubscriptionRoot: singBoxSubscriptionRootDefault, ConfigTemplate: singBoxConfigTemplateDefault, ProviderFile: singBoxProviderFileDefault,
		ProviderUpdateInterval: "24h", ProviderExclude: "节点|剩余|套餐|客服|官网", ProviderHealthCheck: map[string]interface{}{"enabled": true, "url": "http://cp.cloudflare.com/generate_204", "interval": "30m", "timeout": "3s"},
		StartupSync: true, SyncTimeoutSeconds: 90, TestRepeat: 3, TestTimeoutSeconds: 8, DownloadTestBytes: singBoxDefaultDownloadBytes, DownloadTestURL: "https://speed.cloudflare.com/__down?bytes=99999999",
		TestTransport: "mixed", PreferSingTun: false, SingTunRequiresRoot: true, MixedFallback: true, LocalProxyHost: "127.0.0.1", ControllerHost: "127.0.0.1",
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
		cfg.ProviderUpdateInterval = "24h"
	}
	if strings.TrimSpace(cfg.ProviderExclude) == "" {
		cfg.ProviderExclude = "节点|剩余|套餐|客服|官网"
	}
	if cfg.SyncTimeoutSeconds <= 0 || cfg.SyncTimeoutSeconds > 600 {
		cfg.SyncTimeoutSeconds = 90
	}
	if cfg.TestRepeat <= 0 || cfg.TestRepeat > singBoxMaxRepeat {
		cfg.TestRepeat = 3
	}
	if cfg.TestTimeoutSeconds <= 0 || cfg.TestTimeoutSeconds > 60 {
		cfg.TestTimeoutSeconds = 8
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
	if strings.TrimSpace(cfg.TestTransport) == "" {
		cfg.TestTransport = "mixed"
	}
	if strings.TrimSpace(cfg.LocalProxyHost) == "" {
		cfg.LocalProxyHost = "127.0.0.1"
	}
	if strings.TrimSpace(cfg.ControllerHost) == "" {
		cfg.ControllerHost = "127.0.0.1"
	}
	return cfg, nil
}

func singBoxNodesDir(cfg singBoxEngineConfig) string {
	root := cfg.SubscriptionRoot
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return filepath.Join(singBoxDataDir(), root)
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
	name := strings.TrimSpace(subscriptionName)
	if err := validateSubscriptionName(name); err != nil {
		return "", err
	}
	return name + ".json", nil
}

func singBoxNodeFilePath(cfg singBoxEngineConfig, subscriptionName string) (string, error) {
	return singBoxSubscriptionConfigPath(cfg, subscriptionName)
}

func handleSingBoxNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	syncError := ""
	shouldSync := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("sync")), "0")
	if cfg.StartupSync && shouldSync {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.SyncTimeoutSeconds)*time.Second)
		defer cancel()
		if _, e := syncAllSingBoxSubscriptions(ctx, false); e != nil {
			syncError = e.Error()
		}
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	views := make([]singBoxNodeView, 0, len(nodes))
	for _, node := range nodes {
		view := singBoxNodeView{ID: node.ID, Server: node.Server, Port: node.Port, Protocol: node.Protocol, Name: node.Name, Provider: node.Provider, OutboundTag: node.OutboundTag, Sources: node.Sources, SourceCount: len(node.Sources), VariantCount: maxInt(1, len(node.Variants))}
		singBoxState.RLock()
		if result, ok := singBoxState.Results[node.ID]; ok {
			copy := result
			view.LastTest = &copy
		}
		singBoxState.RUnlock()
		views = append(views, view)
	}
	payload := map[string]interface{}{"success": true, "nodes": views}
	if syncError != "" {
		payload["syncError"] = syncError
	}
	writeSingBoxJSON(w, http.StatusOK, payload)
}

func handleSingBoxSyncAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	force := strings.EqualFold(r.URL.Query().Get("force"), "1") || strings.EqualFold(r.URL.Query().Get("force"), "true")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
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

func handleSingBoxTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req singBoxTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if req.Repeat <= 0 || req.Repeat > singBoxMaxRepeat {
		req.Repeat = cfg.TestRepeat
	}
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 60 {
		req.TimeoutSeconds = cfg.TestTimeoutSeconds
	}
	if req.DownloadBytes < 0 {
		req.DownloadBytes = 0
	}
	if req.DownloadBytes == 0 {
		req.DownloadBytes = cfg.DownloadTestBytes
	}
	if strings.TrimSpace(req.DownloadURL) == "" {
		req.DownloadURL = cfg.DownloadTestURL
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var target *singBoxCachedNode
	for i := range nodes {
		if nodes[i].ID == strings.TrimSpace(req.NodeID) {
			copy := nodes[i]
			target = &copy
			break
		}
	}
	if target == nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": "节点不存在，请先同步订阅"})
		return
	}
	result := testCachedSingBoxNode(r.Context(), cfg, *target, req.VariantIndex, req.Repeat, time.Duration(req.TimeoutSeconds)*time.Second, req.DownloadBytes, req.DownloadURL)
	singBoxState.Lock()
	singBoxState.Results[target.ID] = result
	singBoxState.Unlock()
	writeSingBoxJSON(w, http.StatusOK, result)
}

func handleSingBoxBatchTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req singBoxBatchTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
		return
	}
	if len(req.NodeIDs) == 0 {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "没有选择节点"})
		return
	}
	if len(req.NodeIDs) > singBoxMaxBatchNodes {
		req.NodeIDs = req.NodeIDs[:singBoxMaxBatchNodes]
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if req.Repeat <= 0 || req.Repeat > singBoxMaxRepeat {
		req.Repeat = cfg.TestRepeat
	}
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 60 {
		req.TimeoutSeconds = cfg.TestTimeoutSeconds
	}
	if req.DownloadBytes <= 0 {
		req.DownloadBytes = cfg.DownloadTestBytes
	}
	if strings.TrimSpace(req.DownloadURL) == "" {
		req.DownloadURL = cfg.DownloadTestURL
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	wanted := make(map[string]struct{}, len(req.NodeIDs))
	for _, id := range req.NodeIDs {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	selected := make([]singBoxCachedNode, 0, len(wanted))
	for _, node := range nodes {
		if _, ok := wanted[node.ID]; ok {
			selected = append(selected, node)
		}
	}
	if len(selected) == 0 {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": "所选节点不存在"})
		return
	}

	runtime, err := startSingBoxRuntime(r.Context(), cfg, selected)
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer runtime.Close()

	results := make([]singBoxNodeResult, 0, len(selected))
	for _, node := range selected {
		result := testNodeInRuntime(r.Context(), runtime, cfg, node, req.Repeat, time.Duration(req.TimeoutSeconds)*time.Second, req.DownloadBytes, req.DownloadURL)
		singBoxState.Lock()
		singBoxState.Results[node.ID] = result
		singBoxState.Unlock()
		results = append(results, result)
		if r.Context().Err() != nil {
			break
		}
	}
	passed := 0
	for _, result := range results {
		if result.Success {
			passed++
		}
	}
	writeSingBoxJSON(w, http.StatusOK, singBoxBatchTestResponse{Success: passed == len(results), Total: len(results), Passed: passed, Results: results})
}

func mergeConfiguredSingBoxSubscriptions(configured []singBoxConfiguredSubscription) error {
	_ = configured
	return nil
}

func syncAllSingBoxSubscriptions(ctx context.Context, force bool) (singBoxSyncResponse, error) {
	singBoxSyncMu.Lock()
	defer singBoxSyncMu.Unlock()

	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return singBoxSyncResponse{}, err
	}
	if err := os.MkdirAll(singBoxNodesDir(cfg), 0700); err != nil {
		return singBoxSyncResponse{}, fmt.Errorf("创建订阅目录失败: %w", err)
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
		configPath, configErr := singBoxSubscriptionConfigPath(cfg, item.Name)
		if pathErr != nil || configErr != nil {
			if pathErr != nil {
				resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", item.Name, pathErr))
			} else {
				resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", item.Name, configErr))
			}
			continue
		}

		need := force
		if _, e := os.Stat(configPath); e != nil {
			need = true
		}
		if st, e := os.Stat(providerPath); e != nil || !providerCacheLooksValid(providerPath) || time.Since(st.ModTime()) >= interval {
			need = true
		}
		if !need {
			resp.Cached++
			continue
		}

		ctxOne, cancel := context.WithTimeout(ctx, time.Duration(cfg.SyncTimeoutSeconds)*time.Second)
		_, e := syncOneSingBoxSubscription(ctxOne, item, true)
		cancel()
		if e != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", item.Name, e))
			continue
		}
		resp.Updated++
	}

	nodes, e := loadAggregatedSingBoxNodes()
	if e == nil {
		resp.Nodes = len(nodes)
	}
	if len(resp.Errors) > 0 {
		resp.Success = resp.Nodes > 0 || len(items) == 0
	}
	return resp, nil
}

func refreshSubscriptionProvider(ctx context.Context, cfg singBoxEngineConfig, item subscription, path string) error {
	_, err := syncOneSingBoxSubscription(ctx, item, true)
	return err
}

func syncOneSingBoxSubscription(ctx context.Context, item subscription, force bool) (subscriptionSummary, error) {
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return subscriptionSummary{}, err
	}
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

	if !force && providerCacheLooksValid(providerPath) {
		if _, e := os.Stat(configPath); e == nil {
			if st, e := os.Stat(providerPath); e == nil {
				interval := 24 * time.Hour
				if d, de := time.ParseDuration(strings.TrimSpace(cfg.ProviderUpdateInterval)); de == nil && d > 0 {
					interval = d
				}
				if time.Since(st.ModTime()) < interval {
					return updateSubscriptionState(item, "success", "", configPath, providerPath)
				}
			}
		}
	}

	templatePath := filepath.Join(singBoxDataDir(), cfg.ConfigTemplate)
	templateRaw, err := os.ReadFile(templatePath)
	if err != nil {
		return subscriptionSummary{}, fmt.Errorf("读取 reF1nd 模板失败: %w", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(templateRaw, &doc); err != nil {
		return subscriptionSummary{}, fmt.Errorf("解析 reF1nd 模板失败: %w", err)
	}
	providers, ok := doc["providers"].([]interface{})
	if !ok || len(providers) == 0 {
		return subscriptionSummary{}, errors.New("reF1nd 模板缺少 providers")
	}
	provider, ok := providers[0].(map[string]interface{})
	if !ok {
		return subscriptionSummary{}, errors.New("reF1nd Provider1 配置格式错误")
	}
	relProvider, err := singBoxProviderRelativePath(cfg, item.Name)
	if err != nil {
		return subscriptionSummary{}, err
	}
	provider["tag"] = "Provider1"
	provider["type"] = "remote"
	provider["url"] = strings.TrimSpace(item.URL)
	provider["exclude"] = cfg.ProviderExclude
	provider["path"] = relProvider
	provider["update_interval"] = cfg.ProviderUpdateInterval
	provider["health_check"] = cfg.ProviderHealthCheck
	providers[0] = provider
	doc["providers"] = providers
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := atomicWriteFile(configPath, data, 0600); err != nil {
		return subscriptionSummary{}, fmt.Errorf("保存订阅配置失败: %w", err)
	}
	bootstrapPath := filepath.Join(root, fmt.Sprintf(".provider-bootstrap-%d.json", time.Now().UnixNano()))
	logPath := strings.TrimSuffix(bootstrapPath, ".json") + ".log"
	defer os.Remove(bootstrapPath)
	defer os.Remove(logPath)
	// Provider 更新只需要最小配置。尤其在 Android CLI 下，不要把完整 R 配置里的
	// eBPF / auto_detect_interface / rule-set 网络监视一起带进来；这些功能会触发
	// NetworkUpdateMonitor，而普通 Android App UID 无权创建 netlink socket。
	bootstrap := map[string]interface{}{"log": map[string]interface{}{"disabled": false, "level": "info", "output": logPath, "timestamp": true}, "providers": []interface{}{map[string]interface{}{"tag": "Provider1", "type": "remote", "url": strings.TrimSpace(item.URL), "exclude": cfg.ProviderExclude, "path": relProvider, "update_interval": cfg.ProviderUpdateInterval, "health_check": cfg.ProviderHealthCheck}}, "outbounds": []interface{}{map[string]interface{}{"tag": "direct", "type": "direct"}}, "route": map[string]interface{}{"final": "direct"}}
	bd, _ := json.MarshalIndent(bootstrap, "", "  ")
	if err := os.WriteFile(bootstrapPath, bd, 0600); err != nil {
		return subscriptionSummary{}, err
	}
	binary, err := findSingBoxBinary()
	if err != nil {
		return subscriptionSummary{}, err
	}
	if err := runSingBoxCheck(ctx, binary, bootstrapPath, root); err != nil {
		return subscriptionSummary{}, fmt.Errorf("Provider 配置检查失败: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return subscriptionSummary{}, err
	}
	cmd, err := buildSingBoxCommand(ctx, binary, []string{"run", "-c", bootstrapPath}, root, logFile)
	if err != nil {
		logFile.Close()
		return subscriptionSummary{}, fmt.Errorf("启动 Provider 所需 sing-box 失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return subscriptionSummary{}, fmt.Errorf("启动 sing-box Provider 失败: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(time.Duration(cfg.SyncTimeoutSeconds) * time.Second)
	var runErr error
	for time.Now().Before(deadline) {
		if providerCacheLooksValid(providerPath) {
			_ = terminateProcess(cmd)
			<-done
			logFile.Close()
			return updateSubscriptionState(item, "success", "", configPath, providerPath)
		}
		select {
		case e := <-done:
			runErr = e
		case <-time.After(250 * time.Millisecond):
		}
		if runErr != nil {
			break
		}
		if ctx.Err() != nil {
			runErr = ctx.Err()
			break
		}
	}
	_ = terminateProcess(cmd)
	select {
	case <-done:
	default:
	}
	logFile.Close()
	logText, _ := os.ReadFile(logPath)
	if runErr == nil {
		runErr = errors.New("等待 Provider1.json 生成超时")
	}
	if strings.TrimSpace(string(logText)) != "" {
		runErr = fmt.Errorf("%w；核心日志: %s", runErr, lastLogLines(string(logText), 12))
	}
	return updateSubscriptionState(item, "error", runErr.Error(), configPath, providerPath)
}

func runSingBoxCheck(ctx context.Context, binary, configPath, workDir string) error {
	cmd, err := buildSingBoxCommand(ctx, binary, []string{"check", "-c", configPath}, workDir, nil)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
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
		item2.Error = message
		item2.ConfigPath = configPath
		item2.ProviderPath = providerPath
		item2.NodeCount = count
		item2.UpdatedAt = time.Now().Format(time.RFC3339)
		item2.StatusCode = 0
		item2.ContentType = ""
		item2.ContentLength = 0
		item2.Format = "sing-box-provider"
		if err := saveSubscriptionStoreLocked(); err != nil {
			return subscriptionSummary{}, err
		}
		return subscriptionSummaryOf(*item2), nil
	}
	return subscriptionSummary{}, fmt.Errorf("订阅不存在: %s", item.ID)
}

func providerCacheLooksValid(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false
	}
	return len(doc.Outbounds) > 0 || len(doc.Endpoints) > 0
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
		item := subMap[name]
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
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析 Provider 缓存失败: %w", err)
	}
	all := doc.Outbounds
	for _, ep := range doc.Endpoints {
		all = append(all, ep)
	}
	result := make([]singBoxCachedNode, 0, len(all))
	for _, outbound := range all {
		typeName := strings.ToLower(stringValue(outbound["type"]))
		server := stringValue(outbound["server"])
		port := intFromJSONNumber(outbound["server_port"])
		if server == "" || port <= 0 || typeName == "" {
			continue
		}
		tag := stringValue(outbound["tag"])
		if tag == "" {
			tag = stringValue(outbound["name"])
		}
		if tag == "" {
			continue
		}
		name := tag
		if i := strings.LastIndex(tag, "/"); i >= 0 && i+1 < len(tag) {
			name = tag[i+1:]
		}
		source := singBoxNodeSource{SubscriptionID: item.ID, SubscriptionName: item.Name, SubscriptionURL: item.URL, ProviderTag: providerTag, NodeTag: tag}
		node := singBoxCachedNode{ID: singBoxNodeID(server, port), Server: server, Port: port, Protocol: typeName, Name: name, Provider: providerTag, OutboundTag: tag, Outbound: cloneInterfaceMap(outbound), Sources: []singBoxNodeSource{source}, Variants: []singBoxNodeVariant{{Protocol: typeName, Name: name, Server: server, Port: port, Provider: providerTag, OutboundTag: tag, Outbound: cloneInterfaceMap(outbound), Sources: []singBoxNodeSource{source}}}}
		result = append(result, node)
	}
	return result, nil
}

func findRawEndpointForSubscription(content, server string, port int) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	if content == "" {
		return ""
	}
	decoded := decodeMaybeBase64Subscription(content)
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, server+":"+strconv.Itoa(port)) {
			return line
		}
	}
	return ""
}

func testCachedSingBoxNode(parent context.Context, cfg singBoxEngineConfig, node singBoxCachedNode, variantIndex, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	variants := append([]singBoxNodeVariant(nil), node.Variants...)
	if len(variants) == 0 {
		variants = []singBoxNodeVariant{{
			Protocol: node.Protocol, Name: node.Name, Server: node.Server, Port: node.Port,
			Provider: node.Provider, OutboundTag: node.OutboundTag, Outbound: cloneInterfaceMap(node.Outbound), Sources: node.Sources,
		}}
	}

	runtime, err := startSingBoxRuntime(parent, cfg, []singBoxCachedNode{node})
	if err != nil {
		name := node.Name
		protocol := node.Protocol
		if variantIndex >= 0 && variantIndex < len(variants) {
			name = variants[variantIndex].Name
			protocol = variants[variantIndex].Protocol
		}
		return singBoxNodeResult{
			NodeID: node.ID, Node: name, Protocol: protocol, Server: node.Server, Port: node.Port,
			Success: false, TotalAttempts: repeat, LossRate: 100, Mode: "unavailable", Error: err.Error(),
		}
	}
	defer runtime.Close()

	if variantIndex >= 0 && variantIndex < len(variants) {
		return testVariantInRuntime(parent, runtime, cfg, node, variants[variantIndex], repeat, timeout, downloadBytes, downloadURL)
	}

	var last singBoxNodeResult
	for _, variant := range variants {
		result := testVariantInRuntime(parent, runtime, cfg, node, variant, repeat, timeout, downloadBytes, downloadURL)
		if result.Success {
			return result
		}
		last = result
		if parent.Err() != nil {
			break
		}
	}
	if last.NodeID == "" {
		last = singBoxNodeResult{NodeID: node.ID, Node: node.Name, Protocol: node.Protocol, Server: node.Server, Port: node.Port, Mode: runtime.mode, TotalAttempts: repeat, LossRate: 100}
	}
	return last
}

func startSingBoxRuntime(parent context.Context, cfg singBoxEngineConfig, nodes []singBoxCachedNode) (*singBoxRuntime, error) {
	binary, err := findSingBoxBinary()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("没有可用于启动 sing-box 的节点")
	}
	providers := providerRefsForNodes(cfg, nodes)
	if len(providers) == 0 {
		return nil, errors.New("没有可用的 Provider 缓存")
	}

	controllerPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	secret, err := randomControllerSecret()
	if err != nil {
		return nil, err
	}

	tmpDir := singBoxDataDir()
	configPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-test-%d.json", time.Now().UnixNano()))
	logPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-test-%d.log", time.Now().UnixNano()))

	outbounds := []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}}
	if len(providers) > 0 {
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "selector",
			"tag":                         singBoxSelectorTag,
			"providers":                   providers,
			"default":                     "direct",
			"interrupt_exist_connections": true,
		})
	}
	config := map[string]interface{}{
		"providers": providerConfigObjects(providers, cfg),
		"outbounds": outbounds,
		"experimental": map[string]interface{}{
			"clash_api": map[string]interface{}{
				"external_controller": fmt.Sprintf("%s:%d", cfg.ControllerHost, controllerPort),
				"secret":              secret,
			},
		},
		"route": map[string]interface{}{
			"final":                 "direct",
			"auto_detect_interface": true,
		},
	}

	if cfg.PreferSingTun {
		if appUID, ok := configuredAppUID(); ok {
			config["inbounds"] = []interface{}{map[string]interface{}{
				"type":           "tun",
				"tag":            singBoxTunTag,
				"interface_name": fmt.Sprintf("cfdtun-%d", controllerPort%10000),
				"address":        []string{"172.19.0.1/30"},
				"mtu":            1500,
				"auto_route":     true,
				"strict_route":   true,
				"include_uid":    []int{appUID},
			}}
			config["route"].(map[string]interface{})["final"] = singBoxSelectorTag
		}
	}

	fallbackProxyPort := 0
	if !cfg.PreferSingTun || !cfg.SingTunRequiresRoot {
		fallbackProxyPort, err = reserveLocalPort()
		if err != nil {
			return nil, err
		}
		config["inbounds"] = []interface{}{map[string]interface{}{
			"type": "mixed", "tag": singBoxMixedTag, "listen": cfg.LocalProxyHost, "listen_port": fallbackProxyPort,
		}}
		config["route"].(map[string]interface{})["final"] = singBoxSelectorTag
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}

	cmd, mode, err := buildSingBoxRunCommand(parent, binary, configPath, logFile, cfg.PreferSingTun && cfg.SingTunRequiresRoot)
	if err != nil {
		_ = logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		if cfg.MixedFallback {
			return startSingBoxMixedFallback(parent, cfg, nodes)
		}
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		return nil, fmt.Errorf("启动 sing-box 失败: %w", err)
	}

	runtime := &singBoxRuntime{
		process: cmd, configPath: configPath, logPath: logPath, mode: mode,
		controllerURL: fmt.Sprintf("http://%s:%d", cfg.ControllerHost, controllerPort),
		secret:        secret,
	}
	if fallbackProxyPort > 0 {
		runtime.proxyURL = fmt.Sprintf("http://%s:%d", cfg.LocalProxyHost, fallbackProxyPort)
	}

	readyCtx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := waitForClashAPI(readyCtx, runtime.controllerURL, runtime.secret); err != nil {
		runtime.Close()
		if fallback, fallbackErr := startSingBoxMixedFallback(parent, cfg, nodes); fallbackErr == nil {
			return fallback, nil
		}
		return nil, fmt.Errorf("sing-box %s 启动失败: %w", mode, err)
	}
	return runtime, nil
}

func startSingBoxMixedFallback(parent context.Context, cfg singBoxEngineConfig, nodes []singBoxCachedNode) (*singBoxRuntime, error) {
	if !cfg.MixedFallback {
		return nil, errors.New("未启用 mixed fallback")
	}
	binary, err := findSingBoxBinary()
	if err != nil {
		return nil, err
	}
	providers := providerRefsForNodes(cfg, nodes)
	controllerPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	proxyPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	secret, err := randomControllerSecret()
	if err != nil {
		return nil, err
	}
	config := map[string]interface{}{
		"providers": providerConfigObjects(providers, cfg),
		"inbounds": []interface{}{map[string]interface{}{
			"type": "mixed", "tag": singBoxMixedTag, "listen": cfg.LocalProxyHost, "listen_port": proxyPort,
		}},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}, map[string]interface{}{
			"type": "selector", "tag": singBoxSelectorTag, "providers": providers, "default": "direct", "interrupt_exist_connections": true,
		}},
		"experimental": map[string]interface{}{"clash_api": map[string]interface{}{
			"external_controller": fmt.Sprintf("%s:%d", cfg.ControllerHost, controllerPort), "secret": secret,
		}},
		"route": map[string]interface{}{"final": singBoxSelectorTag, "auto_detect_interface": true},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(singBoxDataDir(), fmt.Sprintf(".singbox-mixed-%d.json", time.Now().UnixNano()))
	logPath := filepath.Join(singBoxDataDir(), fmt.Sprintf(".singbox-mixed-%d.log", time.Now().UnixNano()))
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}
	cmd, err := buildSingBoxCommand(parent, binary, []string{"run", "-c", configPath}, singBoxDataDir(), logFile)
	if err != nil {
		logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		return nil, err
	}
	runtime := &singBoxRuntime{
		process: cmd, configPath: configPath, logPath: logPath, mode: "mixed",
		controllerURL: fmt.Sprintf("http://%s:%d", cfg.ControllerHost, controllerPort),
		secret:        secret, proxyURL: fmt.Sprintf("http://%s:%d", cfg.LocalProxyHost, proxyPort),
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := waitForClashAPI(ctx, runtime.controllerURL, runtime.secret); err != nil {
		runtime.Close()
		return nil, fmt.Errorf("mixed fallback 启动失败: %w", err)
	}
	return runtime, nil
}

func buildSingBoxCommand(parent context.Context, binary string, args []string, workDir string, logFile *os.File) (*exec.Cmd, error) {
	useRoot := runtime.GOOS == "android"
	if useRoot {
		suPath := findSuBinary()
		if suPath == "" {
			return nil, errors.New("Android CLI sing-box 需要 Root：未找到 su")
		}
		if !canUseRootSu(suPath) {
			return nil, errors.New("Android CLI sing-box 需要 Root：su 没有获得 UID 0，请给 CFData-WEB 授予 Root 权限")
		}
		command := "cd " + shellQuote(workDir) + " && exec " + shellQuote(binary)
		for _, arg := range args {
			command += " " + shellQuote(arg)
		}
		cmd := exec.CommandContext(parent, suPath, "-c", command)
		if logFile != nil {
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
		return cmd, nil
	}
	cmd := exec.CommandContext(parent, binary, args...)
	cmd.Dir = workDir
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	return cmd, nil
}

func buildSingBoxRunCommand(parent context.Context, binary, configPath string, logFile *os.File, preferRoot bool) (*exec.Cmd, string, error) {
	if preferRoot || runtime.GOOS == "android" {
		suPath := findSuBinary()
		if suPath == "" {
			return nil, "sing-tun", errors.New("未找到 su；sing-tun TUN 需要 root，mixed fallback 将在外层处理")
		}
		if !canUseRootSu(suPath) {
			return nil, "sing-tun", errors.New("当前 su 无法获得 root 权限")
		}
		command := "cd " + shellQuote(singBoxDataDir()) + " && exec " + shellQuote(binary) + " run -c " + shellQuote(configPath)
		cmd := exec.CommandContext(parent, suPath, "-c", command)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		mode := "sing-tun"
		if !preferRoot {
			mode = "mixed-root"
		}
		return cmd, mode, nil
	}
	cmd := exec.CommandContext(parent, binary, "run", "-c", configPath)
	cmd.Dir = singBoxDataDir()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd, "mixed", nil
}

func testNodeInRuntime(parent context.Context, runtime *singBoxRuntime, cfg singBoxEngineConfig, node singBoxCachedNode, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	variants := append([]singBoxNodeVariant(nil), node.Variants...)
	if len(variants) == 0 {
		variants = []singBoxNodeVariant{{
			Protocol: node.Protocol, Name: node.Name, Server: node.Server, Port: node.Port,
			Provider: node.Provider, OutboundTag: node.OutboundTag, Outbound: cloneInterfaceMap(node.Outbound), Sources: node.Sources,
		}}
	}
	var last singBoxNodeResult
	for _, variant := range variants {
		result := testVariantInRuntime(parent, runtime, cfg, node, variant, repeat, timeout, downloadBytes, downloadURL)
		if result.Success {
			return result
		}
		last = result
		if parent.Err() != nil {
			break
		}
	}
	return last
}

func testVariantInRuntime(parent context.Context, runtime *singBoxRuntime, cfg singBoxEngineConfig, node singBoxCachedNode, variant singBoxNodeVariant, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	result := singBoxNodeResult{
		NodeID: node.ID, Node: variant.Name, Protocol: variant.Protocol, Server: variant.Server, Port: variant.Port,
		TotalAttempts: repeat, Mode: runtime.mode,
	}
	if len(variant.OutboundTag) == 0 {
		variant.OutboundTag = node.OutboundTag
	}
	if err := selectSingBoxOutbound(parent, runtime, variant.OutboundTag); err != nil {
		result.LossRate = 100
		result.Error = "选择节点失败: " + err.Error()
		return result
	}

	testURL := "https://speed.cloudflare.com/cdn-cgi/trace"
	var resolved []net.IP
	if runtime.mode == "sing-tun" {
		var err error
		resolved, err = resolveHostIPs(parent, testURL)
		if err != nil {
			result.LossRate = 100
			result.Error = "测试地址解析失败: " + err.Error()
			return result
		}
	}
	client := newSingBoxHTTPClient(timeout, resolved, runtime.mode == "mixed", runtime.proxyURL)
	results := make([]singBoxProbeResult, 0, repeat)
	var latencySum int64
	var minLatency, maxLatency int64
	var firstIP string
	for attempt := 1; attempt <= repeat; attempt++ {
		probe := probeThroughSingBox(client, testURL, resolved, attempt, timeout)
		results = append(results, probe)
		if probe.Success {
			result.SuccessCount++
			latencySum += probe.LatencyMS
			if minLatency == 0 || probe.LatencyMS < minLatency {
				minLatency = probe.LatencyMS
			}
			if probe.LatencyMS > maxLatency {
				maxLatency = probe.LatencyMS
			}
			if firstIP == "" {
				firstIP = probe.OutboundIP
			}
		}
	}
	result.Results = results
	if result.SuccessCount > 0 {
		result.Success = true
		result.MinLatencyMS = minLatency
		result.MaxLatencyMS = maxLatency
		result.AvgLatencyMS = float64(latencySum) / float64(result.SuccessCount)
		result.OutboundIP = firstIP
	}
	result.LossRate = float64(repeat-result.SuccessCount) * 100 / float64(maxInt(1, repeat))
	result.WorkingSource = firstSourceName(variant.Sources)

	if result.Success && downloadBytes > 0 && strings.TrimSpace(downloadURL) != "" {
		resolvedDownload := []net.IP(nil)
		if runtime.mode == "sing-tun" {
			resolvedDownload, _ = resolveHostIPs(parent, downloadURL)
		}
		downloadClient := client
		if runtime.mode == "sing-tun" && len(resolvedDownload) > 0 {
			downloadClient = newSingBoxHTTPClient(timeout, resolvedDownload, false, "")
		}
		if speed := measureSingBoxDownload(downloadClient, downloadURL, resolvedDownload, downloadBytes, timeout); speed != "" {
			result.Speed = speed
		}
	}
	if !result.Success && len(results) > 0 {
		result.Error = firstProbeError(results)
	}
	return result
}

func selectSingBoxOutbound(parent context.Context, runtime *singBoxRuntime, outboundTag string) error {
	outboundTag = strings.TrimSpace(outboundTag)
	if outboundTag == "" {
		return errors.New("节点标签为空")
	}
	body, _ := json.Marshal(map[string]string{"name": outboundTag})
	req, err := http.NewRequestWithContext(parent, http.MethodPut, runtime.controllerURL+"/proxies/"+url.PathEscape(singBoxSelectorTag), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+runtime.secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func waitForClashAPI(ctx context.Context, controllerURL, secret string) error {
	return waitForClashAPIWithContext(ctx, controllerURL, secret, 15*time.Second)
}

func waitForClashAPIWithContext(parent context.Context, controllerURL, secret string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, controllerURL+"/proxies", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, e := http.DefaultClient.Do(req)
			if e == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func newSingBoxHTTPClient(timeout time.Duration, resolved []net.IP, useMixed bool, proxyAddress string) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		DialContext:           dialResolved(resolved),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       10 * time.Second,
	}
	if useMixed && proxyAddress != "" {
		if proxy, err := url.Parse(proxyAddress); err == nil {
			transport.Proxy = http.ProxyURL(proxy)
			transport.DialContext = (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func dialResolved(resolved []net.IP) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		port := "443"
		if _, value, err := net.SplitHostPort(addr); err == nil && value != "" {
			port = value
		}
		dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, ip := range resolved {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("没有解析到 IP")
		}
		return nil, lastErr
	}
}

func resolveHostIPs(parent context.Context, target string) ([]net.IP, error) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("URL 无效")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("DNS 无结果")
	}
	return ips, nil
}

func probeThroughSingBox(client *http.Client, testURL string, resolved []net.IP, attempt int, timeout time.Duration) singBoxProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return singBoxProbeResult{Attempt: attempt, Error: err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return singBoxProbeResult{Attempt: attempt, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	latency := time.Since(start).Milliseconds()
	probe := singBoxProbeResult{Attempt: attempt, LatencyMS: latency, StatusCode: resp.StatusCode}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		probe.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return probe
	}
	if readErr != nil {
		probe.Error = readErr.Error()
		return probe
	}
	probe.Success = true
	probe.OutboundIP = parseTraceIP(string(body))
	_ = resolved
	return probe
}

func measureSingBoxDownload(client *http.Client, testURL string, resolved []net.IP, bytesWanted int64, timeout time.Duration) string {
	_ = resolved
	_ = bytesWanted
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	buf := make([]byte, 32*1024)
	start := time.Now()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	type chunk struct {
		n   int
		err error
	}
	ch := make(chan chunk, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, e := resp.Body.Read(buf)
			select {
			case ch <- chunk{n: n, err: e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	var total int64
	select {
	case <-deadline.C:
	case c := <-ch:
		if c.n > 0 {
			total += int64(c.n)
		}
		if c.err == nil {
			for {
				select {
				case <-deadline.C:
					goto finished
				case c := <-ch:
					if c.n > 0 {
						total += int64(c.n)
					}
					if c.err != nil {
						goto finished
					}
				}
			}
		}
	}
finished:
	cancel()
	resp.Body.Close()
	<-done
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 || total <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f MB/s", float64(total)/1024/1024/elapsed)
}

func parseTraceIP(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "ip="))
		}
	}
	return ""
}

func providerRefsForNodes(cfg singBoxEngineConfig, nodes []singBoxCachedNode) []string {
	set := make(map[string]struct{})
	for _, node := range nodes {
		if node.Provider != "" {
			set[node.Provider] = struct{}{}
		}
		for _, variant := range node.Variants {
			if variant.Provider != "" {
				set[variant.Provider] = struct{}{}
			}
		}
		for _, source := range node.Sources {
			if source.SubscriptionID != "" {
				set[singBoxProviderTag(source.SubscriptionID)] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for tag := range set {
		if providerCacheForTagExists(cfg, tag) {
			result = append(result, tag)
		}
	}
	return result
}

func providerCacheForTagExists(cfg singBoxEngineConfig, providerTag string) bool {
	item := subscriptionByProviderTag(providerTag)
	if item.ID == "" {
		return false
	}
	path, e := singBoxProviderFilePath(cfg, item.Name)
	return e == nil && providerCacheLooksValid(path)
}

func providerConfigObjects(providerTags []string, cfg singBoxEngineConfig) []interface{} {
	result := make([]interface{}, 0, len(providerTags))
	for _, tag := range providerTags {
		item := subscriptionByProviderTag(tag)
		if item.ID == "" {
			continue
		}
		path, err := singBoxProviderFilePath(cfg, item.Name)
		if err != nil {
			continue
		}
		result = append(result, map[string]interface{}{"type": "local", "tag": tag, "path": path})
	}
	return result
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
	h := sha256.Sum256([]byte(strings.TrimSpace(subscriptionID)))
	return "sub-" + hex.EncodeToString(h[:6])
}

func singBoxEndpointKey(server string, port int) string {
	return strings.ToLower(strings.TrimSpace(server)) + ":" + strconv.Itoa(port)
}

func singBoxNodeID(server string, port int) string {
	h := sha256.Sum256([]byte(singBoxEndpointKey(server, port)))
	return "node-" + hex.EncodeToString(h[:8])
}

func cloneInterfaceMap(value map[string]interface{}) map[string]interface{} {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var copy map[string]interface{}
	if err := json.Unmarshal(data, &copy); err != nil {
		return nil
	}
	return copy
}

func uniqueNodeSources(input []singBoxNodeSource) []singBoxNodeSource {
	seen := map[string]struct{}{}
	result := make([]singBoxNodeSource, 0, len(input))
	for _, source := range input {
		key := source.SubscriptionID + "\x00" + source.NodeTag
		if key == "\x00" {
			key = source.SubscriptionName + "\x00" + source.SubscriptionURL
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, source)
	}
	return result
}

func appendUniqueNodeVariants(base []singBoxNodeVariant, additions ...singBoxNodeVariant) []singBoxNodeVariant {
	seen := make(map[string]struct{}, len(base))
	for _, variant := range base {
		seen[variant.Provider+"\x00"+variant.OutboundTag] = struct{}{}
	}
	for _, variant := range additions {
		key := variant.Provider + "\x00" + variant.OutboundTag
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, variant)
	}
	return base
}

func firstSourceName(sources []singBoxNodeSource) string {
	for _, source := range sources {
		if strings.TrimSpace(source.SubscriptionName) != "" {
			return source.SubscriptionName
		}
	}
	return ""
}

func firstProbeError(results []singBoxProbeResult) string {
	for _, item := range results {
		if strings.TrimSpace(item.Error) != "" {
			return item.Error
		}
	}
	return "节点测试失败"
}

func intFromJSONNumber(value interface{}) int {
	switch typed := value.(type) {
	case json.Number:
		v, _ := typed.Int64()
		return int(v)
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case string:
		v, _ := strconv.Atoi(strings.TrimSpace(typed))
		return v
	default:
		return 0
	}
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func configuredAppUID() (int, bool) {
	raw := strings.TrimSpace(os.Getenv("CFDATA_APP_UID"))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func findSingBoxBinary() (string, error) {
	candidates := []string{}
	if value := strings.TrimSpace(os.Getenv("CFDATA_SINGBOX_PATH")); value != "" {
		candidates = append(candidates, value)
	}
	if value, err := exec.LookPath("sing-box"); err == nil {
		candidates = append(candidates, value)
	}
	candidates = append(candidates, filepath.Join(singBoxDataDir(), "sing-box"), filepath.Join(filepath.Dir(os.Args[0]), "sing-box"))
	seen := map[string]struct{}{}
	for _, value := range candidates {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		if info, err := os.Stat(value); err == nil && !info.IsDir() {
			// Do not return a readable-but-non-executable file and let execve()
			// fail later with the much less useful "permission denied".
			if info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return value, nil
		}
	}
	return "", errors.New("未找到 sing-box 核心，请确认 APK 已内置 Android ARM64 reF1nd sing-box")
}

func findSuBinary() string {
	for _, path := range []string{"/system/bin/su", "/system/xbin/su", "/sbin/su", "/data/adb/ksu/bin/su"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	if path, err := exec.LookPath("su"); err == nil {
		return path
	}
	return ""
}

func canUseRootSu(suPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, suPath, "-c", "id").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "uid=0")
}

func reserveLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func randomControllerSecret() (string, error) {
	buf := make([]byte, singBoxControllerSecretBytes)
	for i := range buf {
		buf[i] = byte(time.Now().UnixNano()>>uint((i%8)*8)) ^ byte(i*31+17)
	}
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:12]), nil
}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(os.Interrupt)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil
}

func (runtime *singBoxRuntime) Close() {
	if runtime == nil {
		return
	}
	runtime.closeOnce.Do(func() {
		_ = terminateProcess(runtime.process)
		_ = os.Remove(runtime.configPath)
		_ = os.Remove(runtime.logPath)
	})
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func lastLogLines(text string, maxLines int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, " | ")
	}
	return strings.Join(lines[len(lines)-maxLines:], " | ")
}

func decodeMaybeBase64Subscription(content string) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, content)
	if decoded, err := decodeBase64URL(compact); err == nil {
		decodedText := strings.TrimSpace(decoded)
		if strings.Contains(decodedText, "://") || strings.HasPrefix(decodedText, "{") || strings.HasPrefix(decodedText, "[") {
			return decodedText
		}
	}
	return content
}

func decodeBase64URL(value string) (string, error) {
	if decoded, err := base64DecodeStd(value); err == nil {
		return decoded, nil
	}
	padding := len(value) % 4
	if padding != 0 {
		value += strings.Repeat("=", 4-padding)
	}
	if decoded, err := base64DecodeURLSafe(value); err == nil {
		return decoded, nil
	}
	return "", errors.New("base64 decode failed")
}

func base64DecodeStd(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func base64DecodeURLSafe(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
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
