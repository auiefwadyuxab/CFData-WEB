(function () {
    'use strict';
    if (window.__CFDataSingBoxUI) return;
    window.__CFDataSingBoxUI = true;

    const esc = (v) => String(v == null ? '' : v)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#039;');

    async function api(url, options) {
        const res = await fetch(url, Object.assign({ credentials: 'same-origin' }, options || {}));
        const text = await res.text();
        let data = {};
        try { data = text ? JSON.parse(text) : {}; } catch (_) { data = { error: text || '服务器返回了无效数据' }; }
        if (!res.ok) throw new Error(data.error || data.message || ('HTTP ' + res.status));
        return data;
    }

    function panelHTML() {
        return `
        <div id="cfdataSingBoxPanel" class="section-box" style="margin-top:16px;">
            <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:12px;flex-wrap:wrap;">
                <div>
                    <h3 style="margin:0;">🛰️ reF1nd sing-box 真连接测试</h3>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:6px;">
                        节点文件由 reF1nd Provider 生成到独立目录；同一 server:port 自动合并，并保留所有订阅来源。
                    </div>
                </div>
                <div style="display:flex;gap:8px;flex-wrap:wrap;">
                    <button class="action-btn" id="cfdataSingBoxSync">🔄 同步订阅</button>
                    <button class="action-btn export-btn" id="cfdataSingBoxBatch">🚀 全部真连接测试</button>
                    <button class="action-btn" id="cfdataSingBoxRefresh">↻ 刷新节点</button>
                </div>
            </div>
            <div id="cfdataSingBoxStatus" style="margin-top:12px;padding:10px 12px;border:1px solid var(--border-color);border-radius:10px;color:var(--text-secondary);font-size:12px;">正在加载 Provider 节点……</div>
            <div style="overflow-x:auto;margin-top:12px;">
                <table id="cfdataSingBoxTable" style="width:100%;min-width:920px;border-collapse:collapse;">
                    <thead><tr>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">节点</th>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">地址</th>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">协议</th>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">来源</th>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">真连接结果</th>
                        <th style="padding:10px;text-align:left;border-bottom:1px solid var(--border-color);">操作</th>
                    </tr></thead>
                    <tbody></tbody>
                </table>
            </div>
            <div id="cfdataSingBoxSourceModal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:99999;align-items:center;justify-content:center;padding:20px;">
                <div style="width:min(960px,96vw);max-height:90vh;overflow:auto;background:var(--card-bg);color:var(--text-main);border:1px solid var(--border-color);border-radius:16px;box-shadow:0 20px 60px rgba(0,0,0,.25);padding:16px;">
                    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;">
                        <h3 id="cfdataSingBoxSourceTitle" style="margin:0;">原始输入</h3>
                        <button class="action-btn" id="cfdataSingBoxSourceClose">关闭</button>
                    </div>
                    <div id="cfdataSingBoxSourceBody" style="margin-top:12px;"></div>
                </div>
            </div>
        </div>`;
    }

    function ensurePanel() {
        const host = document.getElementById('subscriptionControls');
        if (!host) return null;
        let panel = document.getElementById('cfdataSingBoxPanel');
        if (!panel) {
            host.insertAdjacentHTML('beforeend', panelHTML());
            panel = document.getElementById('cfdataSingBoxPanel');
            document.getElementById('cfdataSingBoxSync').onclick = () => syncAll(false);
            document.getElementById('cfdataSingBoxBatch').onclick = () => batchTest();
            document.getElementById('cfdataSingBoxRefresh').onclick = () => refreshNodes();
            document.getElementById('cfdataSingBoxSourceClose').onclick = () => closeSources();
            document.getElementById('cfdataSingBoxSourceModal').addEventListener('click', (e) => {
                if (e.target.id === 'cfdataSingBoxSourceModal') closeSources();
            });
        }
        return panel;
    }

    function setStatus(text, type) {
        const el = document.getElementById('cfdataSingBoxStatus');
        if (!el) return;
        el.textContent = text;
        if (type === 'error') {
            el.style.color = 'var(--error-color)';
            el.style.borderColor = 'rgba(239,68,68,.3)';
        } else if (type === 'success') {
            el.style.color = 'var(--success-color)';
            el.style.borderColor = 'rgba(16,185,129,.3)';
        } else {
            el.style.color = 'var(--text-secondary)';
            el.style.borderColor = 'var(--border-color)';
        }
    }

    function resultText(result) {
        if (!result) return '未测试';
        if (!result.success) return '失败' + (result.error ? '：' + result.error : '');
        const loss = Math.round(Number(result.lossRate) || 0);
        const avg = Number(result.avgLatencyMs || 0).toFixed(0);
        const speed = result.speed ? ' · ' + result.speed : '';
        const ip = result.outboundIP ? ' · 出站 ' + result.outboundIP : '';
        const mode = result.mode ? ' · ' + (result.mode === 'sing-tun' ? 'sing-tun' : result.mode === 'mixed' ? 'mixed fallback' : result.mode) : '';
        return `成功 ${result.successCount}/${result.totalAttempts} · 丢包 ${loss}% · 平均 ${avg} ms${speed}${ip}${mode}`;
    }

    function renderNodes(nodes) {
        const body = document.querySelector('#cfdataSingBoxTable tbody');
        if (!body) return;
        if (!nodes.length) {
            body.innerHTML = '<tr><td colspan="6" style="padding:24px;text-align:center;color:var(--text-secondary);">没有可识别节点。请先同步订阅。</td></tr>';
            return;
        }
        body.innerHTML = nodes.map((node) => {
            const sourceCount = Number(node.sourceCount || (node.sources || []).length || 0);
            const variantCount = Number(node.variantCount || 1);
            const last = node.lastTest;
            return `<tr data-node-id="${esc(node.id)}">
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">
                    <div style="font-weight:700;">${esc(node.name || node.server + ':' + node.port)}</div>
                    <div style="font-size:11px;color:var(--text-secondary);margin-top:3px;">${variantCount} 个配置${sourceCount > 1 ? ' · ' + sourceCount + ' 个来源' : ''}${node.outboundTag ? ' · ' + esc(node.outboundTag) : ''}</div>
                </td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);font-family:ui-monospace,monospace;">${esc(node.server)}:${esc(node.port)}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">${esc(node.protocol || '-')}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">
                    <button class="action-btn" onclick="window.__cfdataSingBoxShowSources('${esc(node.id)}')">${sourceCount} 个输入</button>
                </td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);max-width:360px;">${esc(resultText(last))}</td>
                <td style="padding:10px;border-bottom:1px solid var(--border-color);">
                    <button class="action-btn export-btn" onclick="window.__cfdataSingBoxTest('${esc(node.id)}')">🚀 测试</button>
                </td>
            </tr>`;
        }).join('');
        window.__cfdataSingBoxNodes = nodes;
    }

    async function refreshNodes() {
        ensurePanel();
        try {
            setStatus('正在检查节点缓存……');
            const data = await api('/api/subscription/singbox/nodes');
            window.__cfdataSingBoxNodes = data.nodes || [];
            renderNodes(window.__cfdataSingBoxNodes);
            setStatus(`节点 ${window.__cfdataSingBoxNodes.length} 个；同一 IP/域名:端口已经合并来源。`, 'success');
        } catch (err) {
            setStatus('加载失败：' + err.message, 'error');
        }
    }

    async function syncAll(force) {
        ensurePanel();
        const btn = document.getElementById('cfdataSingBoxSync');
        if (btn) btn.disabled = true;
        try {
            setStatus(force ? '正在强制更新全部订阅并生成节点文件……' : '正在检查订阅缓存，需要时才更新……');
            const data = await api('/api/subscription/singbox/sync-all' + (force ? '?force=true' : ''), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
            await refreshNodes();
            const errors = data.errors && data.errors.length ? `；失败 ${data.errors.length} 个` : '';
            setStatus(`同步完成：更新 ${data.updated || 0}，复用缓存 ${data.cached || 0}，节点 ${data.nodes || 0}${errors}`, errors ? 'error' : 'success');
        } catch (err) {
            setStatus('同步失败：' + err.message, 'error');
        } finally {
            if (btn) btn.disabled = false;
        }
    }

    async function testNode(id) {
        const nodes = window.__cfdataSingBoxNodes || [];
        const node = nodes.find((x) => x.id === id);
        if (!node) return;
        setStatus(`正在真实连接 ${node.server}:${node.port} ……`);
        try {
            const data = await api('/api/subscription/singbox/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ nodeId: id, repeat: 3, timeout: 8 })
            });
            node.lastTest = data;
            renderNodes(nodes);
            setStatus(`${node.server}:${node.port}：${resultText(data)}`, data.success ? 'success' : 'error');
        } catch (err) {
            setStatus('真实连接测试失败：' + err.message, 'error');
        }
    }

    async function batchTest() {
        const nodes = window.__cfdataSingBoxNodes || [];
        if (!nodes.length) return setStatus('没有节点可测试', 'error');
        const btn = document.getElementById('cfdataSingBoxBatch');
        if (btn) btn.disabled = true;
        try {
            setStatus(`开始真实测试 ${nodes.length} 个节点；按顺序测试，避免并发污染延迟/网速数据……`);
            const data = await api('/api/subscription/singbox/test-batch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ nodeIds: nodes.map((x) => x.id), repeat: 3, timeout: 8 })
            });
            const map = new Map((data.results || []).map((item) => [item.nodeId, item]));
            nodes.forEach((node) => { if (map.has(node.id)) node.lastTest = map.get(node.id); });
            renderNodes(nodes);
            setStatus(`批量测试完成：${data.passed || 0}/${data.total || nodes.length} 节点至少成功一次。`, data.passed ? 'success' : 'error');
        } catch (err) {
            setStatus('批量真实测试失败：' + err.message, 'error');
        } finally {
            if (btn) btn.disabled = false;
        }
    }

    function showSources(id) {
        const node = (window.__cfdataSingBoxNodes || []).find((x) => x.id === id);
        if (!node) return;
        const modal = document.getElementById('cfdataSingBoxSourceModal');
        const title = document.getElementById('cfdataSingBoxSourceTitle');
        const body = document.getElementById('cfdataSingBoxSourceBody');
        title.textContent = `订阅来源 · ${node.server}:${node.port}`;
        body.innerHTML = (node.sources || []).map((source, index) => `<div style="border:1px solid var(--border-color);border-radius:12px;padding:12px;margin-bottom:10px;">
            <div style="font-weight:700;">#${index + 1} ${esc(source.subscriptionName || source.subscriptionId || '未知订阅')}</div>
            <div style="font-size:12px;color:var(--text-secondary);margin-top:5px;word-break:break-all;">URL：${esc(source.subscriptionUrl || '-')}</div>
            <div style="font-size:12px;color:var(--text-secondary);margin-top:4px;">Provider：${esc(source.providerTag || '-')}<br>节点标签：${esc(source.nodeTag || '-')}</div>
            ${source.raw ? `<pre style="white-space:pre-wrap;word-break:break-all;margin:8px 0 0;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--text-main);">${esc(source.raw)}</pre>` : '<div style="margin-top:8px;font-size:12px;color:var(--text-secondary);">缓存中没有对应原始 URI 文本；该节点实际配置由 reF1nd Provider 解析并保存。</div>'}
        </div>`).join('') || '<div style="color:var(--text-secondary);">没有订阅来源记录。</div>';
        modal.style.display = 'flex';
    }

    function closeSources() {
        const modal = document.getElementById('cfdataSingBoxSourceModal');
        if (modal) modal.style.display = 'none';
    }

    window.__cfdataSingBoxTest = testNode;
    window.__cfdataSingBoxShowSources = showSources;
    window.__cfdataSingBoxRefresh = refreshNodes;

    function boot() {
        if (!document.getElementById('subscriptionControls')) {
            setTimeout(boot, 700);
            return;
        }
        ensurePanel();
        refreshNodes();
        // Panel is injected into the existing subscription page. Re-ensure it after mode switches.
        setInterval(() => {
            if (document.getElementById('subscriptionControls')) ensurePanel();
        }, 2000);
    }

    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
    else boot();
})();
