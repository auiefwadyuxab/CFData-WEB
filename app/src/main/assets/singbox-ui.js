(function () {
    'use strict';
    if (window.__CFDataSingBoxUI) return;
    window.__CFDataSingBoxUI = true;

    const AUTO_SPEED_URL = 'auto';
    const CLOUDFLARE_SPEED_URL = 'https://speed.cloudflare.com/__down?bytes=99999999';
    const CM_SPEED_URL = 'https://cf.090227.xyz/__down?bytes=99999999';
    const MOBILE_SPEED_URL = 'https://speed.okl.abrdns.com';
    const DEFAULT_TRACE_URL = 'https://speed.cloudflare.com/cdn-cgi/trace';
    const LATENCY_REPEAT = 3;
    const LATENCY_TIMEOUT_SECONDS = 3;
    const SPEED_DURATION_SECONDS = 6;
    const INTER_SPEED_PAUSE_MS = 1200;

    const esc = (v) => String(v == null ? '' : v)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#039;');

    async function api(url, options) {
        const response = await fetch(url, Object.assign({ credentials: 'same-origin' }, options || {}));
        const text = await response.text();
        let data = {};
        try {
            data = text ? JSON.parse(text) : {};
        } catch (_) {
            data = { error: text || '服务器返回了无效数据' };
        }
        if (!response.ok) {
            throw new Error(data.syncError || data.error || data.message || `HTTP ${response.status}`);
        }
        return data;
    }

    const state = {
        editId: '',
        visible: false,
        subscriptions: [],
        nodes: [],
        running: false,
        speedURL: ''
    };

    function panelHTML() {
        return `
        <div id="cfdataSingBoxPanel" class="section-box" style="margin-top:0;">
            <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:12px;flex-wrap:wrap;">
                <div>
                    <h3 style="margin:0;">🛰️ reF1nd sing-box R</h3>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:6px;line-height:1.75;max-width:900px;">
                        reF1nd / SFA 风格 Libbox 负责真实代理连接；CFData 只负责节点整理、重复测试、排序、测速和结果展示。<br>
                        <b>真延迟</b> = 3 次独立真实 HTTP 请求经指定 outbound 到 CF trace；不再使用 TCPing、HTTPing 或 TLS 倍率。<br>
                        <b>测速</b> = 延迟排序完成后，按排序逐节点经同一个 working outbound 连续下载 6 秒，沿用 CFData 的窗口测速思路。
                    </div>
                </div>
                <div style="display:flex;gap:8px;flex-wrap:wrap;">
                    <button class="action-btn" id="cfSBForce">🔄 更新全部</button>
                    <button class="action-btn export-btn" id="cfSBBatch">🚀 全部真连接测试</button>
                    <button class="action-btn" id="cfSBRefresh">↻ 刷新</button>
                </div>
            </div>

            <div style="margin-top:14px;padding:14px;border:1px solid var(--border-color);border-radius:12px;">
                <div style="font-weight:700;margin-bottom:10px;">添加 / 编辑 sing-box R Provider</div>
                <div style="display:grid;grid-template-columns:minmax(160px,240px) 1fr;gap:10px;">
                    <input id="cfSBName" maxlength="120" placeholder="订阅名称，例如：主订阅">
                    <input id="cfSBUrl" type="url" placeholder="https://example.com/subscription">
                </div>
                <div style="margin-top:10px;">
                    <textarea id="cfSBHeaders" rows="3" spellcheck="false" placeholder='可选：订阅请求头 JSON，例如 {"User-Agent":"v2rayNG/2.2.6","Connection":"close","Accept-Encoding":"gzip"}' style="width:100%;box-sizing:border-box;resize:vertical;min-height:72px;"></textarea>
                    <div style="font-size:11px;color:var(--text-secondary);margin-top:5px;line-height:1.6;">
                        这里只影响“拉取订阅”的 HTTP 请求头，不会改变真连接测试的请求。留空即可。
                    </div>
                </div>
                <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
                    <button class="action-btn export-btn" id="cfSBSave">💾 保存并同步</button>
                    <button class="action-btn" id="cfSBSaveOnly">只保存</button>
                    <button class="action-btn" id="cfSBCancel">取消编辑</button>
                    <span id="cfSBEditHint" style="font-size:12px;color:var(--text-secondary);"></span>
                </div>
            </div>

            <div style="margin-top:12px;padding:14px;border:1px solid var(--border-color);border-radius:12px;">
                <div style="font-weight:700;margin-bottom:8px;">⚡ 真连接测速参数</div>
                <div style="display:grid;grid-template-columns:160px minmax(220px,1fr);gap:10px;align-items:center;">
                    <div style="font-size:12px;color:var(--text-secondary);">延迟测试</div>
                    <div style="font-size:12px;">3 次独立真实 HTTP 请求 · 单次超时 3 秒 · 无倍率</div>
                    <div style="font-size:12px;color:var(--text-secondary);">排序</div>
                    <div style="font-size:12px;">成功优先 → 丢包率低 → 平均真延迟低 → 最大真延迟低</div>
                    <div style="font-size:12px;color:var(--text-secondary);">下载测速</div>
                    <div style="font-size:12px;">延迟排序后逐节点测速 · 连续下载 6 秒 · 按真实 MB/s 比较</div>
                    <div style="font-size:12px;color:var(--text-secondary);">测速地址</div>
                    <div style="display:flex;gap:8px;flex-wrap:wrap;">
                        <select id="cfSBSpeedUrlPreset" style="min-width:160px;flex:0 0 auto;">
                            <option value="auto">自动选择</option>
                            <option value="${CLOUDFLARE_SPEED_URL}">Cloudflare</option>
                            <option value="${CM_SPEED_URL}">CM提供</option>
                            <option value="${MOBILE_SPEED_URL}">移动专属</option>
                            <option value="custom">自定义</option>
                        </select>
                        <input id="cfSBSpeedUrl" type="text" value="" placeholder="测速 URL，例如 speed.okl.abrdns.com" style="flex:1;min-width:220px;">
                        <button class="action-btn" id="cfSBSpeedUrlReset" type="button">↺</button>
                    </div>
                </div>
            </div>

            <div id="cfSBStatus" style="margin-top:12px;padding:10px 12px;border:1px solid var(--border-color);border-radius:10px;color:var(--text-secondary);font-size:12px;line-height:1.6;">
                正在加载 sing-box R 订阅状态……
            </div>

            <div style="overflow-x:auto;margin-top:12px;">
                <table id="cfSBSubTable" style="width:100%;min-width:960px;border-collapse:collapse;">
                    <thead><tr>
                        <th>订阅</th><th>URL</th><th>状态</th><th>节点数</th><th>配置文件</th><th>Provider 文件</th><th>操作</th>
                    </tr></thead>
                    <tbody></tbody>
                </table>
            </div>

            <div style="margin-top:16px;display:flex;justify-content:space-between;gap:8px;align-items:center;flex-wrap:wrap;">
                <div style="font-weight:700;">Provider1 节点 <span id="cfSBNodeCount" class="badge">0</span></div>
                <div style="font-size:12px;color:var(--text-secondary);">同 server:port 自动合并，多订阅来源保留；测试沿用每个节点实际 outbound</div>
            </div>
            <div style="overflow-x:auto;margin-top:10px;">
                <table id="cfSBNodeTable" style="width:100%;min-width:1120px;border-collapse:collapse;">
                    <thead><tr>
                        <th>节点</th><th>地址</th><th>协议</th><th>来源</th><th>真连接结果</th><th>操作</th>
                    </tr></thead>
                    <tbody></tbody>
                </table>
            </div>

            <div id="cfSBModal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:99999;align-items:center;justify-content:center;padding:20px;">
                <div style="width:min(960px,96vw);max-height:90vh;overflow:auto;background:var(--card-bg);color:var(--text-main);border:1px solid var(--border-color);border-radius:16px;padding:16px;">
                    <div style="display:flex;justify-content:space-between;align-items:center;gap:12px;">
                        <h3 id="cfSBModalTitle" style="margin:0;">来源</h3>
                        <button class="action-btn" id="cfSBModalClose">关闭</button>
                    </div>
                    <div id="cfSBModalBody" style="margin-top:12px;"></div>
                </div>
            </div>
        </div>`;
    }

    function ensure() {
        const host = document.getElementById('singboxRControls');
        if (!host) return false;
        if (!document.getElementById('cfdataSingBoxPanel')) host.innerHTML = panelHTML();
        return true;
    }

    function appendMainLog(message) {
        const text = String(message == null ? '' : message).trim();
        if (!text) return;
        try {
            if (typeof window.log === 'function') {
                window.log(text);
                return;
            }
        } catch (_) {}
        const box = document.getElementById('logBox');
        if (!box) return;
        const line = document.createElement('div');
        line.textContent = text;
        box.appendChild(line);
        if (box.dataset.cfAutoScroll !== '0') box.scrollTop = box.scrollHeight;
    }

    function installLogTools() {
        const box = document.getElementById('logBox');
        if (!box || box.dataset.cfToolsInstalled === '1') return;
        box.dataset.cfToolsInstalled = '1';
        box.dataset.cfAutoScroll = '1';
        box.style.height = '210px';
        box.style.maxHeight = '60vh';
        box.style.minHeight = '120px';
        box.style.resize = 'vertical';
        box.style.overflowY = 'auto';
        box.style.overflowX = 'auto';
        box.style.userSelect = 'text';
        box.style.webkitUserSelect = 'text';
        const toolbar = document.createElement('div');
        toolbar.id = 'cfLogToolbar';
        toolbar.style.cssText = 'display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-top:16px;margin-bottom:8px;padding:8px 10px;border:1px solid var(--border-color);border-radius:10px;background:var(--bg-color);color:var(--text-secondary);font-size:12px;';
        toolbar.innerHTML = '<b style="color:var(--text-main);">系统日志</b>' +
            '<label style="display:flex;align-items:center;gap:6px;">高度 <input id="cfLogHeight" type="range" min="120" max="560" step="10" value="210" style="width:150px;min-height:0;padding:0;"></label>' +
            '<label style="display:flex;align-items:center;gap:5px;"><input id="cfLogAutoScroll" type="checkbox" checked style="width:auto;min-height:0;"> 自动滚底</label>' +
            '<button class="action-btn" id="cfLogCopy">📋 复制全部</button>' +
            '<button class="action-btn" id="cfLogClear">🧹 清空</button>';
        box.parentNode.insertBefore(toolbar, box);
        document.getElementById('cfLogHeight')?.addEventListener('input', (event) => { box.style.height = `${event.target.value}px`; });
        document.getElementById('cfLogAutoScroll')?.addEventListener('change', (event) => {
            box.dataset.cfAutoScroll = event.target.checked ? '1' : '0';
            if (event.target.checked) box.scrollTop = box.scrollHeight;
        });
        document.getElementById('cfLogCopy')?.addEventListener('click', async () => {
            const text = box.innerText || box.textContent || '';
            try {
                await navigator.clipboard.writeText(text);
                appendMainLog('✅ 系统日志已复制到剪贴板');
            } catch (_) {
                const ta = document.createElement('textarea');
                ta.value = text;
                ta.style.position = 'fixed';
                ta.style.opacity = '0';
                document.body.appendChild(ta);
                ta.select();
                let ok = false;
                try { ok = document.execCommand('copy'); } catch (__) {}
                ta.remove();
                appendMainLog(ok ? '✅ 系统日志已复制到剪贴板' : '❌ 无法直接访问剪贴板，请长按日志文本复制');
            }
        });
        document.getElementById('cfLogClear')?.addEventListener('click', () => { box.innerHTML = '<div>[系统] 日志已清空，等待后续操作...</div>'; });
        const observer = new MutationObserver(() => { if (box.dataset.cfAutoScroll !== '0') box.scrollTop = box.scrollHeight; });
        observer.observe(box, { childList: true, subtree: true, characterData: true });
    }

    function status(text, bad) {
        const element = document.getElementById('cfSBStatus');
        if (element) {
            element.textContent = text;
            element.style.color = bad ? 'var(--error-color)' : 'var(--text-secondary)';
        }
        if (bad || /^(正在|同步|更新|保存|删除|测试|开始|完成|已|节点)/.test(String(text || ''))) appendMainLog((bad ? '❌ ' : 'ℹ️ ') + String(text || ''));
    }

    function subStatus(item) {
        const stateText = item.status || '未同步';
        const color = stateText === 'success' ? 'var(--success-color)' : stateText === 'error' ? 'var(--error-color)' : 'var(--text-secondary)';
        const count = Number(item.nodeCount || 0);
        return `<span style="color:${color};font-weight:700;">${esc(stateText)}</span>${count ? ` · ${count} 节点` : ''}`;
    }

    async function loadSubs() {
        const data = await api('/api/subscription/singbox/subscriptions');
        state.subscriptions = Array.isArray(data.subscriptions) ? data.subscriptions : [];
        const body = document.querySelector('#cfSBSubTable tbody');
        if (!body) return;
        body.innerHTML = state.subscriptions.length ? state.subscriptions.map((item) => {
            const id = esc(item.id || '');
            return `<tr>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);"><b>${esc(item.name || '(未命名)')}</b></td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${esc(item.url || '')}">${esc(item.url || '')}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">${subStatus(item)}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">${Number(item.nodeCount || 0)}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);font-size:11px;word-break:break-all;">${esc(item.configPath || '-')}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);font-size:11px;word-break:break-all;">${esc(item.providerPath || '-')}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);"><div style="display:flex;gap:6px;flex-wrap:wrap;">
                    <button class="action-btn" onclick="window.__cfSBEdit('${id}')">编辑</button>
                    <button class="action-btn export-btn" onclick="window.__cfSBSync('${id}')">更新</button>
                    <button class="action-btn" onclick="window.__cfSBDiagnose('${id}')">诊断</button>
                    <button class="action-btn" onclick="window.__cfSBDelete('${id}')">删除</button>
                </div></td>
            </tr>`;
        }).join('') : '<tr><td colspan="7" style="padding:24px;text-align:center;color:var(--text-secondary);">还没有订阅，请先添加一个 URL。</td></tr>';
    }

    function bridgeCall(method, payload) {
        if (!window.CFDataAndroid || typeof window.CFDataAndroid[method] !== 'function') throw new Error('当前页面没有 Android Libbox Bridge；请在 CFData-WEB Android APK 中使用 sing-box R');
        const raw = window.CFDataAndroid[method](JSON.stringify(payload || {}));
        let data;
        try { data = raw ? JSON.parse(raw) : {}; } catch (_) { throw new Error('Android Libbox 返回了无效数据'); }
        if (!data.success && data.error) throw new Error(data.error);
        return data;
    }

    async function notifySyncState(item, result) {
        try {
            await api('/api/subscription/singbox/sync-state', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id: item.id, success: !!result.success, nodeCount: Number(result.nodeCount || 0), error: result.error || '' })
            });
        } catch (_) {}
    }

    async function syncCore(ids) {
        const configsData = await api('/api/subscription/singbox/configs');
        const wanted = Array.isArray(ids) && ids.length ? new Set(ids) : null;
        const configs = (configsData.configs || []).filter((entry) => entry.success && (!wanted || wanted.has(entry.id)));
        if (!configs.length) throw new Error('没有可交给 Android Libbox Core 同步的订阅配置');
        const result = bridgeCall('singBoxSyncProviders', { configs, timeoutSeconds: 90 });
        const results = Array.isArray(result.results) ? result.results : [];
        for (const item of results) {
            const sub = state.subscriptions.find((entry) => entry.id === item.id);
            if (sub) await notifySyncState(sub, item);
        }
        if (Array.isArray(result.errors) && result.errors.length) throw new Error(result.errors.join('；'));
        return result;
    }

    function parseHeaders() {
        const text = document.getElementById('cfSBHeaders')?.value.trim() || '';
        if (!text) return {};
        let value;
        try { value = JSON.parse(text); } catch (e) { throw new Error(`订阅请求头不是有效 JSON：${e.message}`); }
        if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error('订阅请求头必须是 JSON 对象');
        const headers = {};
        Object.entries(value).forEach(([key, val]) => {
            const k = String(key || '').trim();
            const v = String(val == null ? '' : val).trim();
            if (k && v) headers[k] = v;
        });
        return headers;
    }

    function saveSpeedURLToLocalStorage() {
        try { localStorage.setItem('cfdata-r-speed-url', state.speedURL || ''); } catch (_) {}
    }

    function loadSpeedURLFromLocalStorage() {
        try { return localStorage.getItem('cfdata-r-speed-url') || ''; } catch (_) { return ''; }
    }

    function normalizeSpeedURL(raw) {
        let value = String(raw || '').trim();
        if (!value || value === '自动选择') return resolveAutoSpeedURL();
        if (value === AUTO_SPEED_URL) return resolveAutoSpeedURL();
        if (value.startsWith('//')) value = `https:${value}`;
        if (!/^https?:\/\//i.test(value)) value = `https://${value}`;
        return value;
    }

    function resolveAutoSpeedURL() {
        const globalDefault = String(window.__CFDataDefaultSpeedTestURL || '').trim();
        return normalizeExplicitURL(globalDefault || MOBILE_SPEED_URL);
    }

    function normalizeExplicitURL(value) {
        let out = String(value || '').trim();
        if (!out) return '';
        if (out.startsWith('//')) out = `https:${out}`;
        if (!/^https?:\/\//i.test(out)) out = `https://${out}`;
        return out;
    }

    function resolveRSpeedUrl() {
        const preset = document.getElementById('cfSBSpeedUrlPreset')?.value || AUTO_SPEED_URL;
        const input = document.getElementById('cfSBSpeedUrl')?.value.trim() || '';
        if (preset === AUTO_SPEED_URL) return resolveAutoSpeedURL();
        if (preset === 'custom') return normalizeExplicitURL(input || resolveAutoSpeedURL());
        return normalizeExplicitURL(preset);
    }

    function syncRSpeedUrlUI() {
        const select = document.getElementById('cfSBSpeedUrlPreset');
        const input = document.getElementById('cfSBSpeedUrl');
        if (!select || !input) return;
        if (select.value === AUTO_SPEED_URL) {
            input.value = resolveAutoSpeedURL();
            input.disabled = true;
            input.style.opacity = '0.7';
        } else {
            input.disabled = false;
            input.style.opacity = '1';
            if (select.value !== 'custom') input.value = normalizeExplicitURL(select.value);
        }
        state.speedURL = resolveRSpeedUrl();
        saveSpeedURLToLocalStorage();
    }

    function restoreRSpeedUrlUI() {
        const saved = loadSpeedURLFromLocalStorage();
        const select = document.getElementById('cfSBSpeedUrlPreset');
        const input = document.getElementById('cfSBSpeedUrl');
        if (!select || !input) return;
        const url = saved || resolveAutoSpeedURL();
        const normalized = normalizeExplicitURL(url);
        if (!saved || normalized === resolveAutoSpeedURL()) {
            select.value = AUTO_SPEED_URL;
        } else if (normalized === CLOUDFLARE_SPEED_URL || normalized === CM_SPEED_URL || normalized === MOBILE_SPEED_URL) {
            select.value = normalized;
        } else {
            select.value = 'custom';
        }
        input.value = normalized;
        syncRSpeedUrlUI();
    }

    async function save(sync) {
        const name = document.getElementById('cfSBName')?.value.trim() || '';
        const url = document.getElementById('cfSBUrl')?.value.trim() || '';
        if (!name || !url) return status('订阅名称和 URL 不能为空', true);
        try {
            const headers = parseHeaders();
            status(sync ? '正在保存订阅并交给 Android Libbox Core 更新……' : '正在保存……');
            const data = await api('/api/subscription/singbox/save', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id: state.editId, name, url, headers, sync: false })
            });
            const savedId = data.subscription?.id || state.editId;
            cancel();
            await loadSubs();
            if (sync && savedId) {
                const coreResult = await syncCore([savedId]);
                const synced = (coreResult.results || []).find((entry) => entry.id === savedId);
                status(`保存成功，Libbox Provider 已更新；节点 ${Number(synced?.nodeCount || 0)}`, false);
            } else {
                status('保存成功，等待 Android Libbox Core 同步', false);
            }
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`保存失败：${error.message}`, true);
        }
    }

    async function diagnose(id) {
        try {
            const data = await api(`/api/subscription/singbox/diagnostics?id=${encodeURIComponent(id)}`);
            const providerState = data.providerExists ? `存在，${Number(data.providerBytes || 0)} bytes` : '不存在';
            const valid = data.providerValid ? '有效 JSON 节点缓存' : '无效/空缓存';
            const nodeCount = Number(data.nodeCount || 0);
            appendMainLog(`🔎 诊断「${data.name || id}」：Provider1 ${providerState}，${valid}，节点 ${nodeCount}`);
            appendMainLog(`📁 配置：${data.configPath || '-'}；Provider：${data.providerPath || '-'}`);
            if (data.providerParseError) appendMainLog(`❌ Provider 解析：${data.providerParseError}`);
            if (data.logTail && String(data.logTail).trim() && data.logTail !== '（暂无 Provider1.update.log）') {
                appendMainLog('🧾 最后一次 Provider 核心日志：');
                String(data.logTail).split(/\n| \| /).forEach(line => { const t = String(line).trim(); if (t) appendMainLog(t); });
            }
            status(`诊断完成：${nodeCount} 个节点` + (data.providerExists ? '' : '，Provider1.json 不存在'), nodeCount === 0);
        } catch (error) {
            status(`诊断失败：${error.message}`, true);
        }
    }

    async function syncOne(id) {
        try {
            status('正在准备订阅并交给 Android Libbox Provider 更新……');
            await api('/api/subscription/singbox/update', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id })
            });
            const result = await syncCore([id]);
            const synced = (result.results || []).find((entry) => entry.id === id);
            status(`Libbox Provider 更新${synced?.success ? '成功' : '失败'}：节点 ${Number(synced?.nodeCount || 0)}`, !synced?.success);
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`更新失败：${error.message}`, true);
        }
    }

    async function syncAll() {
        try {
            status('正在把全部订阅交给 Android Libbox Provider 更新……');
            const subs = await api('/api/subscription/singbox/subscriptions');
            state.subscriptions = Array.isArray(subs.subscriptions) ? subs.subscriptions : [];
            const result = await syncCore(state.subscriptions.map((entry) => entry.id));
            const errors = Array.isArray(result.errors) ? result.errors : [];
            if (errors.length) status(`Libbox 更新完成，但有 ${errors.length} 个失败：${errors[0]}`, true);
            else status(`Libbox 更新完成：${(result.results || []).filter((entry) => entry.success).length} 个订阅成功`, false);
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`同步失败：${error.message}`, true);
        }
    }

    async function deleteOne(id) {
        if (!confirm('确定删除这个 sing-box R 订阅及其配置、Provider1 节点缓存吗？')) return;
        try {
            await api('/api/subscription/singbox/delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id })
            });
            status('订阅已删除');
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`删除失败：${error.message}`, true);
        }
    }

    function edit(id) {
        const item = state.subscriptions.find((entry) => entry.id === id);
        if (!item) return;
        state.editId = id;
        document.getElementById('cfSBName').value = item.name || '';
        document.getElementById('cfSBUrl').value = item.url || '';
        document.getElementById('cfSBHeaders').value = item.headers && Object.keys(item.headers).length ? JSON.stringify(item.headers, null, 2) : '';
        document.getElementById('cfSBEditHint').textContent = `正在编辑：${item.name || id}`;
        document.getElementById('cfSBName').focus();
    }

    function cancel() {
        state.editId = '';
        const name = document.getElementById('cfSBName');
        const url = document.getElementById('cfSBUrl');
        const headers = document.getElementById('cfSBHeaders');
        const hint = document.getElementById('cfSBEditHint');
        if (name) name.value = '';
        if (url) url.value = '';
        if (headers) headers.value = '';
        if (hint) hint.textContent = '';
    }

    function latencySortEntries(nodes) {
        return [...nodes].sort((a, b) => {
            const ar = a.lastTest || {};
            const br = b.lastTest || {};
            const as = ar.success ? 0 : 1;
            const bs = br.success ? 0 : 1;
            if (as !== bs) return as - bs;
            const al = Number(ar.lossRate ?? 100);
            const bl = Number(br.lossRate ?? 100);
            if (al !== bl) return al - bl;
            const aa = Number(ar.avgLatencyMs ?? Number.POSITIVE_INFINITY);
            const ba = Number(br.avgLatencyMs ?? Number.POSITIVE_INFINITY);
            if (aa !== ba) return aa - ba;
            const am = Number(ar.maxLatencyMs ?? Number.POSITIVE_INFINITY);
            const bm = Number(br.maxLatencyMs ?? Number.POSITIVE_INFINITY);
            if (am !== bm) return am - bm;
            return String(a.name || '').localeCompare(String(b.name || ''), 'zh-Hans');
        });
    }

    function renderNodes(nodes) {
        state.nodes = Array.isArray(nodes) ? nodes : [];
        const count = document.getElementById('cfSBNodeCount');
        if (count) count.textContent = String(state.nodes.length);
        const body = document.querySelector('#cfSBNodeTable tbody');
        if (!body) return;
        if (!state.nodes.length) {
            body.innerHTML = '<tr><td colspan="6" style="padding:24px;text-align:center;color:var(--text-secondary);">没有可识别节点。请先更新 Provider1，并确认节点数量大于 0。</td></tr>';
            return;
        }
        body.innerHTML = state.nodes.map((node) => `<tr>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);"><b>${esc(node.name || '-')}</b></td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);font-family:ui-monospace,monospace;">${esc(node.server || '-')} : ${esc(node.port || '-')}</td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);">${esc(node.protocol || '-')}</td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);"><button class="action-btn" onclick="window.__cfSBSource('${esc(node.id)}')">${Number(node.sourceCount || 0)} 个来源</button></td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);">${resultText(node.lastTest)}</td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);"><button class="action-btn export-btn" ${state.running ? 'disabled' : ''} onclick="window.__cfSBTest('${esc(node.id)}')">🚀 测试</button></td>
        </tr>`).join('');
    }

    function resultText(result) {
        if (!result) return '未测试';
        const loss = Math.round(Number(result.lossRate || 0));
        const avg = result.avgLatencyMs != null ? Number(result.avgLatencyMs).toFixed(0) : '-';
        const dial = result.outboundDialMs ? ` · 建连 ${Number(result.outboundDialMs).toFixed(0)}ms` : '';
        const tls = result.tlsHandshakeMs ? ` · TLS ${Number(result.tlsHandshakeMs).toFixed(0)}ms` : '';
        const speed = result.speedSuccess ? ` · ${esc(result.speed || '-')}` : (result.speedError ? ` · 测速失败：${esc(result.speedError)}` : '');
        const outbound = result.outboundIP ? ` · 出站 ${esc(result.outboundIP)}` : '';
        const colo = result.colo ? ` · ${esc(result.colo)}` : '';
        const variant = result.testedOutboundTag && result.testedOutboundTag !== result.outboundTag ? ` · 路径 ${esc(result.testedOutboundTag)}` : '';
        const attempts = `${Number(result.successCount || 0)}/${Number(result.totalAttempts || 0)}`;
        if (!result.success) return `失败 · ${attempts} · 丢包 ${loss}% · ${esc(result.error || '未知错误')}`;
        return `成功 ${attempts} · 丢包 ${loss}% · 真延迟 ${avg}ms${dial}${tls}${speed}${outbound}${colo}${variant}`;
    }

    async function refreshNodes(autoSync) {
        try {
            const data = await api('/api/subscription/singbox/nodes' + (autoSync ? '' : '?sync=0'));
            renderNodes(data.nodes || []);
            if (data.syncError) status(`节点已加载，但自动同步失败：${data.syncError}`, true);
        } catch (error) {
            status(`节点加载失败：${error.message}`, true);
        }
    }

    function nodePayload(node) {
        return {
            id: node.id,
            name: node.name,
            protocol: node.protocol,
            server: node.server,
            port: node.port,
            outboundTag: node.outboundTag,
            outbound: node.outbound,
            variants: Array.isArray(node.variants) ? node.variants : []
        };
    }

    function candidatesForNode(node) {
        const candidates = [];
        const seen = new Set();
        const add = (tag, sources) => {
            tag = String(tag || '').trim();
            if (!tag || seen.has(tag)) return;
            seen.add(tag);
            candidates.push({ outboundTag: tag, sources: Array.isArray(sources) ? sources : [] });
        };
        add(node.outboundTag, node.sources);
        (node.variants || []).forEach(variant => add(variant.outboundTag, variant.sources));
        return candidates;
    }

    function putWorkingSource(result, candidate) {
        const source = (candidate.sources || [])[0] || {};
        const sourceName = String(source.subscriptionName || '').trim();
        const nodeTag = String(source.nodeTag || '').trim();
        const text = sourceName && nodeTag ? `${sourceName} / ${nodeTag}` : sourceName || nodeTag;
        if (text) result.workingSource = text;
    }

    function mergeSpeedIntoNode(node, latencyResult, speedResult) {
        const merged = Object.assign({}, latencyResult || {});
        merged.speedSuccess = !!speedResult?.success;
        merged.speed = speedResult?.speed || '';
        merged.speedMBps = Number(speedResult?.speedMBps || 0);
        merged.speedDurationMs = Number(speedResult?.speedDurationMs || 0);
        merged.speedBytes = Number(speedResult?.speedBytes || 0);
        merged.speedStatusCode = Number(speedResult?.statusCode || 0);
        merged.speedError = speedResult?.error || '';
        merged.testedOutboundTag = latencyResult?.testedOutboundTag || node.outboundTag || '';
        if (speedResult?.outboundIP) merged.outboundIP = speedResult.outboundIP;
        node.lastTest = merged;
        return merged;
    }

    function latencyTestNode(node) {
        const candidates = candidatesForNode(node);
        if (!candidates.length) throw new Error('节点缺少可用 outbound');
        let lastResult = null;
        const errors = [];
        for (let index = 0; index < candidates.length; index++) {
            const candidate = candidates[index];
            try {
                const data = bridgeCall('singBoxTrueLatencyTest', {
                    outboundTag: candidate.outboundTag,
                    testURL: DEFAULT_TRACE_URL,
                    repeat: LATENCY_REPEAT,
                    timeout: LATENCY_TIMEOUT_SECONDS
                });
                const result = Object.assign({}, data);
                result.nodeId = node.id;
                result.node = node.name || node.id;
                result.protocol = node.protocol;
                result.server = node.server;
                result.port = node.port;
                result.testedOutboundTag = candidate.outboundTag;
                result.variantAttempts = index + 1;
                putWorkingSource(result, candidate);
                lastResult = result;
                if (result.success) return result;
                errors.push(`${candidate.outboundTag}: ${result.error || '3次真实请求均失败'}`);
            } catch (error) {
                errors.push(`${candidate.outboundTag}: ${error.message}`);
            }
        }
        return Object.assign(lastResult || {}, {
            nodeId: node.id,
            node: node.name || node.id,
            protocol: node.protocol,
            server: node.server,
            port: node.port,
            testedOutboundTag: '',
            variantAttempts: candidates.length,
            success: false,
            successCount: 0,
            totalAttempts: LATENCY_REPEAT,
            lossRate: 100,
            error: errors.join('；') || '所有可用 outbound 均测试失败'
        });
    }

    function speedTestNode(node, latencyResult) {
        const outboundTag = String(latencyResult?.testedOutboundTag || node.outboundTag || '').trim();
        if (!outboundTag) return { success: false, error: '没有可用于测速的 working outbound' };
        const url = resolveRSpeedUrl();
        if (!url) return { success: false, error: '测速 URL 为空' };
        try {
            return bridgeCall('singBoxTrueSpeedTest', {
                outboundTag,
                downloadUrl: url,
                duration: SPEED_DURATION_SECONDS
            });
        } catch (error) {
            return { success: false, error: error.message };
        }
    }

    async function testNode(id) {
        if (state.running) return;
        const node = state.nodes.find((entry) => entry.id === id);
        if (!node || !node.outbound) return status('节点缺少 reF1nd outbound 配置，请重新同步 Provider1', true);
        state.running = true;
        try {
            status(`${node.name || id}：正在做 ${LATENCY_REPEAT} 次真实 HTTP 延迟测试……`);
            const latency = latencyTestNode(node);
            node.lastTest = latency;
            renderNodes(state.nodes);
            if (!latency.success) {
                status(`${node.name || id}：真连接延迟测试失败`, true);
                return;
            }
            status(`${node.name || id}：延迟 ${Number(latency.avgLatencyMs || 0).toFixed(0)}ms，开始连续 ${SPEED_DURATION_SECONDS} 秒测速……`);
            const speed = speedTestNode(node, latency);
            mergeSpeedIntoNode(node, latency, speed);
            renderNodes(state.nodes);
            status(`${node.name || id}：${resultText(node.lastTest)}`, !speed.success);
        } catch (error) {
            status(`真连接测试失败：${error.message}`, true);
        } finally {
            state.running = false;
            renderNodes(state.nodes);
        }
    }

    async function batch() {
        if (state.running) return;
        if (!state.nodes.length) return status('没有节点可测试', true);
        const nodes = state.nodes.filter((node) => node.outbound);
        if (!nodes.length) return status('当前节点没有可用的 reF1nd outbound', true);
        state.running = true;
        try {
            status(`第一阶段：${nodes.length} 个节点逐个做 ${LATENCY_REPEAT} 次真实 HTTP 延迟测试……`);
            let completed = 0;
            for (const node of nodes) {
                if (!state.running) break;
                const result = latencyTestNode(node);
                node.lastTest = result;
                completed++;
                if (completed === nodes.length || completed % 5 === 0) {
                    renderNodes(latencySortEntries(state.nodes));
                    status(`真实延迟阶段：${completed}/${nodes.length}`);
                }
            }
            state.nodes = latencySortEntries(state.nodes);
            renderNodes(state.nodes);
            const latencyPassed = state.nodes.filter((node) => node.lastTest?.success && node.lastTest?.testedOutboundTag);
            status(`第一阶段完成：${latencyPassed.length}/${nodes.length} 个节点通过真实延迟；现在按这个排序进入 ${SPEED_DURATION_SECONDS} 秒下载测速……`);

            const speedURL = resolveRSpeedUrl();
            appendMainLog(`⚡ 真连接测速地址：${speedURL}`);
            for (let i = 0; i < latencyPassed.length; i++) {
                const node = latencyPassed[i];
                const latency = node.lastTest;
                status(`第二阶段：${i + 1}/${latencyPassed.length} · ${node.name || node.id} · ${Number(latency.avgLatencyMs || 0).toFixed(0)}ms → ${SPEED_DURATION_SECONDS}s 下载……`);
                const speed = speedTestNode(node, latency);
                mergeSpeedIntoNode(node, latency, speed);
                if (i === latencyPassed.length - 1 || (i + 1) % 3 === 0) renderNodes(state.nodes);
                if (i < latencyPassed.length - 1) await new Promise(resolve => setTimeout(resolve, INTER_SPEED_PAUSE_MS));
            }
            renderNodes(state.nodes);
            const speedOK = state.nodes.filter(node => node.lastTest?.speedSuccess).length;
            status(`批量真连接测试完成：${latencyPassed.length}/${nodes.length} 通过延迟，${speedOK}/${latencyPassed.length} 完成测速。`, false);
        } catch (error) {
            status(`批量测试失败：${error.message}`, true);
        } finally {
            state.running = false;
            renderNodes(state.nodes);
        }
    }

    function source(id) {
        const node = state.nodes.find((entry) => entry.id === id);
        const modal = document.getElementById('cfSBModal');
        if (!node || !modal) return;
        document.getElementById('cfSBModalTitle').textContent = `${node.server}:${node.port} · 来源`;
        document.getElementById('cfSBModalBody').innerHTML = (node.sources || []).map((item) => `
            <div style="padding:10px;border:1px solid var(--border-color);border-radius:10px;margin-bottom:8px;">
                <b>${esc(item.subscriptionName || '')}</b>
                <div style="font-size:12px;color:var(--text-secondary);word-break:break-all;margin-top:5px;">${esc(item.subscriptionUrl || '')}</div>
                <div style="font-size:12px;color:var(--text-secondary);margin-top:4px;">Provider：${esc(item.providerTag || '')} · 节点标签：${esc(item.nodeTag || '')}</div>
            </div>`).join('') || '无来源记录';
        modal.style.display = 'flex';
    }

    function setVisible(visible) {
        state.visible = !!visible;
        if (!ensure()) return;
        const host = document.getElementById('singboxRControls');
        if (host) host.classList.toggle('hidden', !state.visible);
        installLogTools();
        if (state.visible) {
            restoreRSpeedUrlUI();
            loadSubs().catch((error) => status(`订阅读取失败：${error.message}`, true));
            refreshNodes(false);
        }
    }

    window.__cfSBSetVisible = setVisible;
    window.__cfSBEdit = edit;
    window.__cfSBSync = syncOne;
    window.__cfSBDiagnose = diagnose;
    window.__cfSBDelete = deleteOne;
    window.__cfSBTest = testNode;
    window.__cfSBSource = source;

    function boot() {
        if (!ensure()) return;
        installLogTools();
        document.getElementById('cfSBSave').onclick = () => save(true);
        document.getElementById('cfSBSaveOnly').onclick = () => save(false);
        document.getElementById('cfSBCancel').onclick = cancel;
        document.getElementById('cfSBForce').onclick = syncAll;
        document.getElementById('cfSBBatch').onclick = batch;
        document.getElementById('cfSBRefresh').onclick = () => { loadSubs(); refreshNodes(false); };
        document.getElementById('cfSBModalClose').onclick = () => { document.getElementById('cfSBModal').style.display = 'none'; };
        document.getElementById('cfSBSpeedUrlPreset').onchange = syncRSpeedUrlUI;
        document.getElementById('cfSBSpeedUrl').oninput = () => {
            const select = document.getElementById('cfSBSpeedUrlPreset');
            if (select && select.value !== AUTO_SPEED_URL && select.value !== 'custom') select.value = 'custom';
            state.speedURL = normalizeExplicitURL(document.getElementById('cfSBSpeedUrl').value);
            saveSpeedURLToLocalStorage();
        };
        document.getElementById('cfSBSpeedUrlReset').onclick = () => {
            document.getElementById('cfSBSpeedUrlPreset').value = AUTO_SPEED_URL;
            syncRSpeedUrlUI();
        };
        setVisible(false);
    }

    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
    else boot();
})();
