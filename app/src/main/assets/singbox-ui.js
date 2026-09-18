(function () {
    'use strict';
    if (window.__CFDataSingBoxUI) return;
    window.__CFDataSingBoxUI = true;

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
    };

    function panelHTML() {
        return `
        <div id="cfdataSingBoxPanel" class="section-box" style="margin-top:0;">
            <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:12px;flex-wrap:wrap;">
                <div>
                    <h3 style="margin:0;">🛰️ reF1nd sing-box R</h3>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:6px;line-height:1.7;">
                        这里是唯一的订阅入口。订阅由 reF1nd Provider1 负责拉取和生成节点缓存；真连接测试沿用 CFData 的测试目标与测速逻辑，只替换实际代理出站。
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
                <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
                    <button class="action-btn export-btn" id="cfSBSave">💾 保存并同步</button>
                    <button class="action-btn" id="cfSBSaveOnly">只保存</button>
                    <button class="action-btn" id="cfSBCancel">取消编辑</button>
                    <span id="cfSBEditHint" style="font-size:12px;color:var(--text-secondary);"></span>
                </div>
            </div>

            <div id="cfSBStatus" style="margin-top:12px;padding:10px 12px;border:1px solid var(--border-color);border-radius:10px;color:var(--text-secondary);font-size:12px;line-height:1.6;">
                正在加载 sing-box R 订阅状态……
            </div>

            <div style="overflow-x:auto;margin-top:12px;">
                <table id="cfSBSubTable" style="width:100%;min-width:900px;border-collapse:collapse;">
                    <thead><tr>
                        <th>订阅</th><th>URL</th><th>状态</th><th>节点数</th><th>配置文件</th><th>Provider 文件</th><th>操作</th>
                    </tr></thead>
                    <tbody></tbody>
                </table>
            </div>

            <div style="margin-top:16px;display:flex;justify-content:space-between;gap:8px;align-items:center;flex-wrap:wrap;">
                <div style="font-weight:700;">Provider1 节点 <span id="cfSBNodeCount" class="badge">0</span></div>
                <div style="font-size:12px;color:var(--text-secondary);">同 server:port 自动合并，多订阅来源保留</div>
            </div>
            <div style="overflow-x:auto;margin-top:10px;">
                <table id="cfSBNodeTable" style="width:100%;min-width:980px;border-collapse:collapse;">
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

        document.getElementById('cfLogHeight')?.addEventListener('input', (event) => {
            box.style.height = `${event.target.value}px`;
        });
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
        document.getElementById('cfLogClear')?.addEventListener('click', () => {
            box.innerHTML = '<div>[系统] 日志已清空，等待后续操作...</div>';
        });
        const observer = new MutationObserver(() => {
            if (box.dataset.cfAutoScroll !== '0') box.scrollTop = box.scrollHeight;
        });
        observer.observe(box, { childList: true, subtree: true, characterData: true });
    }

    function status(text, bad) {
        const element = document.getElementById('cfSBStatus');
        if (element) {
            element.textContent = text;
            element.style.color = bad ? 'var(--error-color)' : 'var(--text-secondary)';
        }
        if (bad || /^(正在|同步|更新|保存|删除|测试|开始|完成|已|节点)/.test(String(text || ''))) {
            appendMainLog((bad ? '❌ ' : 'ℹ️ ') + String(text || ''));
        }
    }

    function subStatus(item) {
        const state = item.status || '未同步';
        const color = state === 'success' ? 'var(--success-color)' : state === 'error' ? 'var(--error-color)' : 'var(--text-secondary)';
        const count = Number(item.nodeCount || 0);
        return `<span style="color:${color};font-weight:700;">${esc(state)}</span>${count ? ` · ${count} 节点` : ''}`;
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

    async function save(sync) {
        const name = document.getElementById('cfSBName')?.value.trim() || '';
        const url = document.getElementById('cfSBUrl')?.value.trim() || '';
        if (!name || !url) return status('订阅名称和 URL 不能为空', true);
        try {
            status(sync ? '正在保存并通过 reF1nd Provider1 更新……' : '正在保存……');
            const data = await api('/api/subscription/singbox/save', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id: state.editId, name, url, headers: {}, sync }),
            });
            if (data.syncError) {
                status(`已保存，但 Provider1 同步失败：${data.syncError}`, true);
            } else {
                const count = Number(data.subscription?.nodeCount || 0);
                status(`保存成功${sync ? '，Provider1 已更新' : ''}${sync ? `；节点 ${count}` : ''}`, false);
            }
            cancel();
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`保存失败：${error.message}`, true);
        }
    }

    async function diagnose(id) {
        try {
            const data = await api(`/api/subscription/singbox/diagnostics?id=${encodeURIComponent(id)}`);
            const providerState = data.providerExists
                ? `存在，${Number(data.providerBytes || 0)} bytes`
                : '不存在';
            const valid = data.providerValid ? '有效 JSON 节点缓存' : '无效/空缓存';
            const nodeCount = Number(data.nodeCount || 0);
            appendMainLog(`🔎 诊断「${data.name || id}」：Provider1 ${providerState}，${valid}，节点 ${nodeCount}`);
            appendMainLog(`📁 配置：${data.configPath || '-'}；Provider：${data.providerPath || '-'}`);
            if (data.providerParseError) appendMainLog(`❌ Provider 解析：${data.providerParseError}`);
            if (data.logTail && String(data.logTail).trim() && data.logTail !== '（暂无 Provider1.update.log）') {
                appendMainLog('🧾 最后一次 Provider 核心日志：');
                String(data.logTail).split(/\n| \| /).forEach(line => {
                    const t = String(line).trim();
                    if (t) appendMainLog(t);
                });
            }
            status(`诊断完成：${nodeCount} 个节点` + (data.providerExists ? '' : '，Provider1.json 不存在'), nodeCount === 0);
        } catch (error) {
            status(`诊断失败：${error.message}`, true);
        }
    }

    async function syncOne(id) {
        try {
            status('正在用 reF1nd sing-box R Provider1 更新订阅……');
            const data = await api('/api/subscription/singbox/update', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id, force: true }),
            });
            const count = Number(data.subscription?.nodeCount || 0);
            status(`更新成功，Provider1 节点 ${count}`, false);
            await loadSubs();
            await refreshNodes(false);
        } catch (error) {
            status(`更新失败：${error.message}`, true);
        }
    }

    async function syncAll() {
        try {
            status('正在强制更新全部 sing-box R Provider1……');
            const data = await api('/api/subscription/singbox/sync-all?force=true', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: '{}',
            });
            if (Array.isArray(data.errors) && data.errors.length) {
                status(`同步完成，但有 ${data.errors.length} 个失败；节点 ${Number(data.nodes || 0)}。首个错误：${data.errors[0]}`, true);
            } else {
                status(`同步完成：更新 ${Number(data.updated || 0)}，缓存 ${Number(data.cached || 0)}，节点 ${Number(data.nodes || 0)}`, false);
            }
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
                body: JSON.stringify({ id }),
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
        document.getElementById('cfSBEditHint').textContent = `正在编辑：${item.name || id}`;
        document.getElementById('cfSBName').focus();
    }

    function cancel() {
        state.editId = '';
        const name = document.getElementById('cfSBName');
        const url = document.getElementById('cfSBUrl');
        const hint = document.getElementById('cfSBEditHint');
        if (name) name.value = '';
        if (url) url.value = '';
        if (hint) hint.textContent = '';
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
            <td style="padding:10px;border-bottom:1px solid var(--border-color);">${esc(resultText(node.lastTest))}</td>
            <td style="padding:10px;border-bottom:1px solid var(--border-color);"><button class="action-btn export-btn" onclick="window.__cfSBTest('${esc(node.id)}')">🚀 测试</button></td>
        </tr>`).join('');
    }

    function resultText(result) {
        if (!result) return '未测试';
        if (!result.success) return `失败：${result.error || '未知错误'}`;
        const loss = Math.round(Number(result.lossRate || 0));
        const avg = Number(result.avgLatencyMs || 0).toFixed(0);
        const speed = result.speed ? ` · ${esc(result.speed)}` : '';
        const outbound = result.outboundIP ? ` · 出站 ${esc(result.outboundIP)}` : '';
        return `成功 ${Number(result.successCount || 0)}/${Number(result.totalAttempts || 0)} · 丢包 ${loss}% · 平均 ${avg} ms${speed}${outbound}`;
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

    async function testNode(id) {
        try {
            const data = await api('/api/subscription/singbox/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ nodeId: id, repeat: 3, timeout: 8 }),
            });
            const node = state.nodes.find((entry) => entry.id === id);
            if (node) node.lastTest = data;
            renderNodes(state.nodes);
            status(`${data.node || id}：${resultText(data)}`, !data.success);
        } catch (error) {
            status(`真连接测试失败：${error.message}`, true);
        }
    }

    async function batch() {
        if (!state.nodes.length) return status('没有节点可测试', true);
        try {
            status(`开始测试 ${state.nodes.length} 个节点；按节点顺序执行，保持低干扰……`);
            const data = await api('/api/subscription/singbox/test-batch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ nodeIds: state.nodes.map((node) => node.id), repeat: 3, timeout: 8 }),
            });
            const byID = new Map((data.results || []).map((result) => [result.nodeId, result]));
            state.nodes.forEach((node) => { if (byID.has(node.id)) node.lastTest = byID.get(node.id); });
            renderNodes(state.nodes);
            status(`批量测试完成：${Number(data.passed || 0)}/${Number(data.total || state.nodes.length)} 节点成功。`, !(data.passed || 0));
        } catch (error) {
            status(`批量测试失败：${error.message}`, true);
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
        setVisible(false);
    }

    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
    else boot();
})();
