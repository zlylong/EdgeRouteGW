// Global error reporting: uncaught errors and unhandled promise rejections are
// surfaced as a red toast once the app has set up (console before that).
let reportError = (msg) => console.error(msg);
window.onerror = function(msg, url, line, col, error) { reportError("JS Error: " + msg + " at line " + line); };
window.addEventListener('unhandledrejection', function(ev) {
    const reason = ev.reason;
    if (reason && reason.apiFetchHandled) return; // apiFetch already toasted / raised the connectivity banner
    const text = reason && reason.message ? reason.message : String(reason);
    reportError('未处理的异常: ' + text);
});

        const { createApp, ref, computed, onMounted, watch, nextTick } = Vue;

        const app = createApp({
            setup() {
                                const showNodeModal = ref(false);
                const editingNode = ref({ id: null, name: '', type: 'Vmess', address: '', port: 443, uuid: '', params: '{}' });
                const vlessParams = ref({ type: 'tcp', security: 'none', sni: '', pbk: '', fp: 'chrome', sid: '', flow: '', method: 'aes-256-gcm' });
                
                const syslogs = ref({ proxygw: '', xray: '', mosdns: '', frr: '', nftables: '', alerts: '', nft: '' });
                const currentLogTab = ref('proxygw');
                // Query knobs for /api/logs/:service and /api/events (P5.5): how many
                // lines and how far back. Empty since means "no time filter".
                const logLines = ref('200');
                const logSince = ref('');
                const logQuery = () => {
                    const p = new URLSearchParams();
                    if (logLines.value) p.set('lines', logLines.value);
                    if (logSince.value) p.set('since', logSince.value);
                    const q = p.toString();
                    return q ? '?' + q : '';
                };
                const lockScroll = ref(false);
                const syslogsContainer = ref(null);
                
                
                const scrollToBottom = (force = false) => {
                    if (lockScroll.value && !force) return;
                    const el = syslogsContainer.value;

                    if (!el) return;
                    
                    // If not forced, only scroll if user is already near bottom (50px threshold)
                    const isAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50;
                    if (force || isAtBottom) {
                        el.scrollTop = el.scrollHeight;
                    }
                };



                const fetchSyslogs = async (service, forceScroll = false) => {
                    if (!token.value) return;
                    try {
                        if (service === 'events') {
                            const res = await apiFetch('/api/events?limit=200');
                            if (res.ok) {
                                const data = await res.json();
                                if (data.success && Array.isArray(data.events)) {
                                    syslogs.value.events = data.events.map(ev => `[${ev.ts}] [${String(ev.level || '').toUpperCase()}] [${ev.module}/${ev.event_type}] ${ev.message}${ev.trace_id ? ' trace=' + ev.trace_id : ''}${ev.path ? ' path=' + ev.path : ''}${ev.status ? ' status=' + ev.status : ''}${ev.duration_ms ? ' cost=' + ev.duration_ms + 'ms' : ''}${ev.details ? ' details=' + ev.details : ''}`).join('\n');
                                }
                            }
                        } else if (service === 'alerts') {
                            const res = await apiFetch('/api/events?limit=' + encodeURIComponent(logLines.value || '200') + '&level=warn' + (logSince.value ? '&since=' + encodeURIComponent(logSince.value) : ''));
                            if (res.ok) {
                                const data = await res.json();
                                if (data.success && Array.isArray(data.events)) {
                                    // Alerts (Gateway Events) are queried DESC from DB, but we reverse them here to show new logs at the bottom
                                    syslogs.value.alerts = data.events.reverse().map(ev => `[${ev.ts}] [${String(ev.level || '').toUpperCase()}] [${ev.module}/${ev.event_type}] ${ev.message}${ev.trace_id ? ' trace=' + ev.trace_id : ''}${ev.path ? ' path=' + ev.path : ''}${ev.status ? ' status=' + ev.status : ''}${ev.duration_ms ? ' cost=' + ev.duration_ms + 'ms' : ''}${ev.details ? ' details=' + ev.details : ''}`).join('\n');
                                    nextTick(() => scrollToBottom(forceScroll));
                                }
                            }
                        } else if (service === 'nft') {
                            const res = await apiFetch('/api/nftables/stats');
                            if (res.ok) {
                                const data = await res.json();
                                if (data.success) {
                                    const summary = data.summary || {};
                                    const pt = summary.proxy_total || { packets: 0, bytes: 0 };
                                    const dt = summary.direct_total || { packets: 0, bytes: 0 };
                                    const pd = summary.proxy_delta || { packets: 0, bytes: 0 };
                                    const dd = summary.direct_delta || { packets: 0, bytes: 0 };
                                    const lines = [];
                                    lines.push(`updated_at=${data.updated_at || ''}`);
                                    lines.push(`proxy_total packets=${pt.packets} bytes=${pt.bytes}`);
                                    lines.push(`direct_total packets=${dt.packets} bytes=${dt.bytes}`);
                                    lines.push(`proxy_delta(30s) packets=${pd.packets} bytes=${pd.bytes}`);
                                    lines.push(`direct_delta(30s) packets=${dd.packets} bytes=${dd.bytes}`);
                                    lines.push('---- counters ----');
                                    (data.counters || []).forEach(item => {
                                        lines.push(`${item.name}: packets=${item.packets} bytes=${item.bytes}`);
                                    });
                                    syslogs.value.nft = lines.join('\n');
                                    nextTick(() => scrollToBottom(forceScroll));
                                }
                            }
                        } else {
                            const res = await apiFetch(`/api/logs/${service}` + logQuery());
                            if (res.ok) {
                                const data = await res.json();
                                if (data.success) {
                                    syslogs.value[service] = data.logs;
                                    nextTick(() => scrollToBottom(forceScroll));
                                }
                            }
                        }
                        
                    } catch (e) {}
                };

                const traffic = ref({ speed: { up: 0, down: 0 }, total_month: { up: 0, down: 0 }, node_ranking: [] });
                const fetchTraffic = async () => {
                    if (!token.value) return;
                    try {
                        const res = await apiFetch('/api/traffic');
                        if (res.ok) traffic.value = await res.json();
                    } catch (e) {}
                };

                const isSubmitting = ref({ apply: false, update: false, node: false, login: false });
                const isLoading = ref({ apply: false, save: false });

                
                const showAclModal = ref(false);
                const aclForm = ref({ type: 'mac', value: '', policy: 'direct' });

                const openAddAcl = () => { aclForm.value = { type: 'mac', value: '', policy: 'direct' }; showAclModal.value = true; };
                const saveAcl = async () => {
                    isSubmitting.value.acl = true;
                    try {
                        let r = await apiFetch('/api/lan_acls', { method: 'POST', body: JSON.stringify(aclForm.value) });
                        if(r.ok) { showToast('添加成功，防火墙已热重载'); showAclModal.value = false; loadData(false); }
                        else { let d = await r.json().catch(() => ({})); showToast(d.error || '添加失败', 'error'); }
                    } catch(e) { showToast('网络错误', 'error'); }
                    finally { isSubmitting.value.acl = false; }
                };
                const deleteAcl = (id) => customConfirm('确认删除此设备策略？', async () => {
                    const r = await apiJSON('/api/lan_acls/' + id, { method: 'DELETE' });
                    if (!r.ok) { showApiError(r, '删除失败'); if (r.status === 404) loadData(false); return; }
                    showToast('已删除，防火墙已热重载'); loadData(false);
                });
                const updateLanDefault = async () => {
                    const r = await apiJSON('/api/lan_acls/default_policy', { method: 'POST', body: JSON.stringify({ policy: defaultLanPolicy.value }) });
                    if (!r.ok) { showApiError(r, '全局策略切换失败'); loadData(false); return; }
                    showToast('网关全局策略已切换！防火墙热重载完成。');
                };

                const addProtectedIp = async () => {
                    const v = (protectedIpForm.value.value || '').trim();
                    if (!v) return showToast('请输入 IP/CIDR', 'error');
                    const r = await apiFetch('/api/protected_ips', { method: 'POST', body: JSON.stringify({ value: v, remark: protectedIpForm.value.remark || '' }) });
                    const d = await r.json().catch(() => ({}));
                    if (!r.ok) return showToast(d.error || '添加失败', 'error');
                    protectedIpForm.value = { value: '', remark: '' };
                    showToast('保护IP已添加并热生效');
                    loadData(false);
                };
                const deleteProtectedIp = (id) => customConfirm('确认删除该保护IP？', async () => {
                    const r = await apiJSON('/api/protected_ips/' + id, { method: 'DELETE' });
                    if (!r.ok) { showApiError(r, '删除失败'); if (r.status === 404) loadData(false); return; }
                    showToast('保护IP已删除并热生效');
                    loadData(false);
                });

                const openAddNode = () => {

                    editingNode.value = { id: null, name: 'New Node', type: 'Vmess', address: '1.2.3.4', port: 443, uuid: '', params: '{}' };
                    vlessParams.value = { type: 'tcp', security: 'none', sni: '', pbk: '', fp: 'chrome', sid: '', flow: '', method: 'aes-256-gcm' };
                    showNodeModal.value = true;
                };

                                

                const editNode = (node) => {
                    editingNode.value = { ...node };
                    let p = {};
                    if (node.params && node.params !== '{}') {
                        try {
                            p = JSON.parse(node.params);
                        } catch (e) {}
                    }
                    if (Object.keys(p).length === 0) {
                        editingNode.value.params = "";
                    } else {
                        editingNode.value.params = JSON.stringify(p, null, 2);
                    }
                    showNodeModal.value = true;
                };

                const saveNode = async () => {
                    if (isLoading.value.save) return;
                    isLoading.value.save = true;
                    try {
                        let parsedParams = {};
                        if (editingNode.value.params && editingNode.value.params.trim() !== '') {
                            try {
                                parsedParams = JSON.parse(editingNode.value.params);
                            } catch (e) {
                                showToast("高级配置 (JSON) 格式不正确", "error");
                                isLoading.value.save = false;
                                return;
                            }
                        }
                        
                        const payload = {
                            Name: editingNode.value.name || '',
                            Type: editingNode.value.type || 'Vmess',
                            Address: editingNode.value.address || '',
                            Port: parseInt(editingNode.value.port) || 443,
                            UUID: editingNode.value.uuid || '',
                            Params: JSON.stringify(parsedParams)
                        };
                        
                        let url = '/api/nodes';
                        let method = 'POST';
                        
                        if (editingNode.value.id) {
                            url = `/api/nodes/${editingNode.value.id}`;
                            method = 'PUT';
                        }
                        
                        const res = await apiFetch(url, { method: method, body: JSON.stringify(payload) });
                        if (res.ok) {
                            showToast("节点保存成功！");
                            showNodeModal.value = false;
                            loadData();
                        } else {
                            showToast("保存失败，请检查填写内容", "error");
                        }
                    } catch (e) {
                        showToast("保存出错", "error");
                    } finally {
                        isLoading.value.save = false;
                    }
                };


                const showMosdnsRollbackModal = ref(false);
                const mosdnsVersions = ref([]);
                const selectedMosdnsRollbackVersion = ref('');

                const openMosdnsRollbackModal = () => {
                    showMosdnsRollbackModal.value = true;
                    if (mosdnsVersions.value.length === 0) fetchMosdnsVersions();
                };

                const fetchMosdnsVersions = async () => {
                    try {
                        const res = await apiFetch('/api/mosdns/versions');
                        if (res.ok) {
                            const data = await res.json();
                            if (data.versions) {
                                mosdnsVersions.value = data.versions;
                            }
                        }
                    } catch (e) {
                        showToast("获取 Mosdns 版本列表失败", "error");
                        showMosdnsRollbackModal.value = false;
                    }
                };

                const confirmMosdnsRollback = async () => {
                    if (!selectedMosdnsRollbackVersion.value) return;
                    isUpdating.value['rollback_mosdns_specific'] = true;
                    showToast("Mosdns 安装任务已触发，请稍候...", "success");
                    try {
                        const res = await apiFetch('/api/update/mosdns', {
                            method: 'POST',
                            headers: {'Content-Type': 'application/json'},
                            body: JSON.stringify({ version: selectedMosdnsRollbackVersion.value })
                        });
                        if (res.ok) {
                            showToast('Mosdns 指定版本安装成功！');
                            showMosdnsRollbackModal.value = false;
                            loadData();
                        } else {
                            const data = await res.json().catch(()=>({}));
                            showToast('安装失败: ' + (data.error || '未知错误'), 'error');
                        }
                    } catch (e) {
                        showToast('安装出错', 'error');
                    }
                    isUpdating.value['rollback_mosdns_specific'] = false;
                };

                const showRollbackModal = ref(false);
                const xrayVersions = ref([]);
                const selectedRollbackVersion = ref('');

                const openRollbackModal = () => {
                    showRollbackModal.value = true;
                    if (xrayVersions.value.length === 0) {
                        fetchXrayVersions();
                    }
                };

                const fetchXrayVersions = async () => {
                    try {
                        const res = await apiFetch('/api/xray/versions');
                        if (res.ok) {
                            const data = await res.json();
                            if (data.versions) {
                                xrayVersions.value = data.versions;
                            }
                        }
                    } catch (e) {
                        showToast("获取版本列表失败", "error");
                        showRollbackModal.value = false;
                    }
                };

                const confirmRollback = async () => {
                    if (!selectedRollbackVersion.value) return;
                    isUpdating.value['rollback_specific'] = true;
                    showToast("安装任务已触发，请稍候...", "success");
                    try {
                        const res = await apiFetch('/api/update/xray', {
                            method: 'POST',
                            headers: {'Content-Type': 'application/json'},
                            body: JSON.stringify({ version: selectedRollbackVersion.value })
                        });
                        if (res.ok) {
                            showToast('指定版本安装成功！');
                            showRollbackModal.value = false;
                            loadData();
                        } else {
                            const data = await res.json().catch(()=>({}));
                            showToast('安装失败: ' + (data.error || '未知错误'), 'error');
                        }
                    } catch (e) {
                        showToast('安装出错', 'error');
                    }
                    isUpdating.value['rollback_specific'] = false;
                };

                
                const showPwdModal = ref(false);
                const pwdForm = ref({ old: '', new: '' });
                const logout = async () => {
                    try {
                        if (token.value) await apiFetch("/api/logout", { method: "POST" });
                    } catch (e) {}
                    localStorage.removeItem("token");
                    token.value = "";
                    showToast("已安全退出");
                };
                const changePwd = async () => {
                    if(!pwdForm.value.old || !pwdForm.value.new) return showToast("请填写完整", "error");
                    const res = await apiFetch('/api/password', { method: 'POST', body: JSON.stringify({ Old: pwdForm.value.old, New: pwdForm.value.new }) });
                    const data = await res.json().catch(() => ({}));
                    if(res.ok) {
                        showToast("密码修改成功，请重新登录！");
                        showPwdModal.value = false;
                        setTimeout(logout, 1500);
                    } else {
                        showToast(data.error || "修改失败", "error");
                    }
                };

                const activeTab = ref('system');
                const toastMsg = ref('');
                const toastType = ref('success');
                const isUpdating = ref({});
                let toastTimer = null;
                const showToast = (msg, type = 'success') => {
                    toastMsg.value = msg;
                    toastType.value = type;
                    if (toastTimer !== null) clearTimeout(toastTimer);
                    toastTimer = setTimeout(() => { toastMsg.value = ''; toastTimer = null; }, 3000);
                };
                reportError = (msg) => showToast(msg, 'error');
                // Esc closes the topmost open dialog (confirm first, then history, then the rest).
                window.addEventListener('keydown', (ev) => {
                    if (ev.key !== 'Escape') return;
                    if (confirmData.value.show) { if (!confirmData.value.busy) confirmData.value.show = false; return; }
                    for (const m of [showHistoryModal, showRemoteDetailsModal, showRemoteNodeModal, showBatchModal, showNodeModal, showAclModal, showPwdModal, showRollbackModal, showMosdnsRollbackModal]) {
                        if (m && m.value) { m.value = false; return; }
                    }
                });
                // customConfirm replaces window.confirm everywhere: the callback is
                // awaited and the 确认 button stays disabled (busy) until it settles,
                // so a slow request cannot be double-submitted.
                const confirmData = ref({ show: false, msg: '', onConfirm: null, busy: false });
                const customConfirm = (msg, callback) => {
                    confirmData.value = { show: true, msg, onConfirm: callback, busy: false };
                };
                const doConfirm = async () => {
                    if (confirmData.value.busy) return;
                    const cb = confirmData.value.onConfirm;
                    confirmData.value.busy = true;
                    try {
                        if (cb) await cb();
                    } finally {
                        confirmData.value = { show: false, msg: '', onConfirm: null, busy: false };
                    }
                };


                const token = ref(localStorage.getItem('token') || '');
                const password = ref('');
                const login = async () => { 
                    if (isSubmitting.value.login) return;
                    if (!password.value || !password.value.trim()) {
                        showToast("请输入密码", "error");
                        return;
                    }
                    isSubmitting.value.login = true;
                    try {
                        const res = await fetch('/api/login', { 
                            method: 'POST', 
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ password: password.value.trim() }) 
                        });
                        if(res.ok) {
                            const data = await res.json();
                            token.value = data.token;
                            localStorage.setItem('token', data.token);
                            showToast("登录成功", "success");
                            loadData();
                        } else {
                            try {
                                const errData = await res.json();
                                showToast("登录失败: " + errData.error, "error");
                            } catch(e) {
                                showToast("登录失败: HTTP " + res.status, "error");
                            }
                        }
                    } catch (e) {
                        showToast("网络请求异常", "error");
                    } finally {
                        isSubmitting.value.login = false;
                    }
                };
                
                // backendDown drives the fixed "connection lost" banner. It is raised when a
                // request cannot reach the backend at all (network error) and cleared by the
                // next response of any kind. The network-failure toast fires once per outage.
                const backendDown = ref(false);
                let networkFailureToasted = false;
                const apiFetch = async (url, options = {}) => {
                    if(!options.headers) options.headers = {};
                    options.headers['Authorization'] = 'Bearer ' + token.value;
                    if (typeof options.body === 'string' && !Object.keys(options.headers).some(h => h.toLowerCase() === 'content-type')) {
                        options.headers['Content-Type'] = 'application/json';
                    }
                    try {
                        const res = await fetch(url, options);
                        backendDown.value = false;
                        networkFailureToasted = false;
                        if(res.status === 401) {
                            token.value = '';
                            localStorage.removeItem('token');
                            showToast("登录已过期", "error");
                        }
                        return res;
                    } catch(e) {
                        if (!networkFailureToasted) showToast("网络请求失败", "error");
                        networkFailureToasted = true;
                        backendDown.value = true;
                        try { e.apiFetchHandled = true; } catch (_) {}
                        throw e;
                    }
                };
                // apiJSON: apiFetch plus a tolerant JSON parse. Resolves to { ok, status, data }
                // where data is {} for an empty or non-JSON body, so callers can read data.error safely.
                const apiJSON = async (url, options = {}) => {
                    const res = await apiFetch(url, options);
                    let data = {};
                    try { data = await res.json(); } catch (e) { data = {}; }
                    if (data === null || data === undefined) data = {};
                    return { ok: res.ok, status: res.status, data };
                };
                // Standard failure toast for an apiJSON result (401 is already reported by apiFetch).
                const showApiError = (r, fallback = '操作失败') => {
                    if (r.status === 401) return;
                    showToast((r.data && r.data.error) || fallback, 'error');
                };

                                let ws = null;
                let wsReconnectTimer = null;

                const connectWs = (tab) => {
                    if (ws) { ws.close(); ws = null; }
                    clearTimeout(wsReconnectTimer);
                    // DNS logs WS has been removed in backend for performance.
                };

                watch(activeTab, (newTab) => {
                    loadData();
                    if (newTab === 'configs') loadConfigs(); // configs are fetched on tab entry + the 刷新 button, not on every poll tick
                    if(newTab === 'dns') {
                        dnsLogs.value = [];
                    }
                    connectWs(newTab);
                    if (newTab === 'syslogs') nextTick(() => scrollToBottom(true));
                });

                const tabs = [
                    { id: 'system', icon: 'fa-gauge-high', label: '系统状态' },
                    { id: 'connections', icon: 'fa-satellite-dish', label: '实时连接追踪' },
                    { id: 'nodes', icon: 'fa-server', label: '节点管理' },
                    { id: 'remote_nodes', icon: 'fa-cloud-arrow-up', label: '节点自动部署' },
                    { id: 'rules', icon: 'fa-route', label: '路由分流规则' },
                    { id: 'lan_acls', icon: 'fa-laptop-house', label: '设备分流规则', hideModeBC: true },
                    { id: 'protected_ips', icon: 'fa-shield-halved', label: '规则变更保护IP', hideModeBC: true },
                    { id: 'dns', icon: 'fa-globe', label: 'DNS 设置' },
                    { id: 'ospf', icon: 'fa-project-diagram', label: 'OSPF 状态', hideModeA: true },
                    { id: 'configs', icon: 'fa-file-code', label: '配置信息面板' },
                    { id: 'syslogs', icon: 'fa-terminal', label: '核心日志面板' },
                    { id: 'test_tools', icon: 'fa-vial-circle-check', label: '诊断测试工具' },
                ];
                const hiddenInNormalTabs = new Set(['remote_nodes', 'configs', 'syslogs', 'protected_ips', 'connections']);
                const uiMode = ref(localStorage.getItem('ui_mode') === 'advanced' ? 'advanced' : 'normal');
                const toggleUiMode = () => {
                    uiMode.value = uiMode.value === 'advanced' ? 'normal' : 'advanced';
                    localStorage.setItem('ui_mode', uiMode.value);
                };
                const visibleTabs = computed(() => {
                    const mode = sysStatus.value?.mode;
                    return tabs.filter(t => {
                        if (t.hideModeBC && mode !== 'A') return false;
                        if (t.hideModeA && mode === 'A') return false;
                        if (uiMode.value === 'normal' && hiddenInNormalTabs.has(t.id)) return false;
                        return true;
                    });
                });
                watch([activeTab, uiMode], () => {
                    const ids = visibleTabs.value.map(t => t.id);
                    if (!ids.includes(activeTab.value)) {
                        activeTab.value = 'system';
                    }
                });

                const currentTabLabel = computed(() => tabs.find(t => t.id === activeTab.value)?.label);

                
                
                const lanAcls = ref([]);
                const defaultLanPolicy = ref('proxy');
                const protectedIps = ref([]);
                const protectedIpForm = ref({ value: "", remark: "" });
                const connections = ref([]);

                const connSearch = ref('');
                const filteredConnections = computed(() => {
                    if (!connSearch.value) return connections.value;
                    const s = connSearch.value.toLowerCase();
                    // The placeholder promises client or target IP; match both.
                    return connections.value.filter(c => (c.client || '').toLowerCase().includes(s) || (c.target || '').toLowerCase().includes(s) || (c.target_domain || '').toLowerCase().includes(s));
                });


                const nodes = ref([]);
                const nodeFailoverMode = ref('normal');

                const rules = ref([]);
                const ruleGroups = ref([]);
                const selectedRuleGroup = ref('');
                const ruleSearchId = ref('');
                const editingRuleGroup = ref('');
                const categories = ref({ geosite: [], geoip: [] });
                const geoQuery = ref({ input: '', loading: false, result: null });
                const showGeoSuggestions = ref(false);
                const geoSuggestionIndex = ref(-1);
                const filteredGeoSuggestions = computed(() => {
                    const input = (geoQuery.value.input || '').trim().toLowerCase();
                    if (input.startsWith('geosite:')) {
                        const keyword = input.slice('geosite:'.length);
                        return (categories.value.geosite || []).filter(tag => tag.includes(keyword)).slice(0, 50).map(tag => 'geosite:' + tag);
                    }
                    if (input.startsWith('geoip:')) {
                        const keyword = input.slice('geoip:'.length);
                        return (categories.value.geoip || []).filter(tag => tag.includes(keyword)).slice(0, 50).map(tag => 'geoip:' + tag);
                    }
                    if (input === '') {
                        const geoip = (categories.value.geoip || []).slice(0, 20).map(tag => 'geoip:' + tag);
                        const geosite = (categories.value.geosite || []).slice(0, 20).map(tag => 'geosite:' + tag);
                        return [...new Set([...geosite, ...geoip])];
                    }
                    return [];
                });
                const handleGeoInput = () => {
                    showGeoSuggestions.value = true;
                    geoSuggestionIndex.value = filteredGeoSuggestions.value.length ? 0 : -1;
                };
                const handleGeoInputBlur = () => {
                    setTimeout(() => {
                        showGeoSuggestions.value = false;
                        geoSuggestionIndex.value = -1;
                    }, 120);
                };
                const moveGeoSuggestion = (direction) => {
                    showGeoSuggestions.value = true;
                    const total = filteredGeoSuggestions.value.length;
                    if (!total) {
                        geoSuggestionIndex.value = -1;
                        return;
                    }
                    if (geoSuggestionIndex.value < 0) {
                        geoSuggestionIndex.value = direction > 0 ? 0 : total - 1;
                        return;
                    }
                    geoSuggestionIndex.value = (geoSuggestionIndex.value + direction + total) % total;
                };
                const applyGeoSuggestion = (item) => {
                    geoQuery.value.input = item;
                    showGeoSuggestions.value = false;
                    geoSuggestionIndex.value = -1;
                };
                const confirmGeoSuggestionOrQuery = () => {
                    if (showGeoSuggestions.value && geoSuggestionIndex.value >= 0 && filteredGeoSuggestions.value[geoSuggestionIndex.value]) {
                        applyGeoSuggestion(filteredGeoSuggestions.value[geoSuggestionIndex.value]);
                        return;
                    }
                    runGeoQuery();
                };
                const runGeoQuery = async () => {
                    const input = (geoQuery.value.input || '').trim();
                    if (!input) {
                        showToast('请输入域名/IP 或 geoip:cn / geosite:gfw', 'error');
                        return;
                    }
                    showGeoSuggestions.value = false;
                    geoSuggestionIndex.value = -1;
                    geoQuery.value.loading = true;
                    try {
                        const res = await apiFetch('/api/geo/query?input=' + encodeURIComponent(input));
                        if (!res.ok) {
                            const data = await res.json().catch(() => ({}));
                            showToast(data.error || 'Geo 查询失败', 'error');
                            return;
                        }
                        geoQuery.value.result = await res.json();
                    } catch (e) {
                        showToast('Geo 查询失败', 'error');
                    } finally {
                        geoQuery.value.loading = false;
                    }
                };
                const dns = ref({ local: '', remote: '', lazy: true, mode: 'smart', log_level: 'info', cache_size: 10240, lazy_ttl: 86400 });
                const dnsLogs = ref([]);
                const ospf = ref({ neighbors: 0, published: 0, pending: 0, logs: [] });
                const ospfController = ref({ push_batch_limit: 500, push_interval_seconds: 10, resolve_workers: 16 });
                const ospfControllerDirty = ref(false);
                const applyOspfPayload = (data) => {
                    ospf.value = {
                        neighbors: data?.neighbors || 0,
                        published: data?.published || 0,
                        pending: data?.pending || 0,
                        logs: data?.logs || []
                    };
                    if (!ospfControllerDirty.value) {
                        ospfController.value.push_batch_limit = data?.push_batch_limit || 500;
                        ospfController.value.push_interval_seconds = data?.push_interval_seconds || 10;
                        ospfController.value.resolve_workers = data?.resolve_workers || 16;
                    }
                };
                const saveOspfController = async () => {
                    const payload = {
                        push_batch_limit: parseInt(ospfController.value.push_batch_limit, 10) || 1,
                        push_interval_seconds: parseInt(ospfController.value.push_interval_seconds, 10) || 1,
                        resolve_workers: parseInt(ospfController.value.resolve_workers, 10) || 1
                    };
                    const res = await apiFetch('/api/ospf/settings?confirm=APPLY', { method: 'POST', body: JSON.stringify(payload) });
                    if (!res.ok) {
                        showToast('OSPF 推送控制保存失败', 'error');
                        return;
                    }
                    const data = await res.json().catch(() => ({}));
                    ospfController.value.push_batch_limit = data.push_batch_limit || payload.push_batch_limit;
                    ospfController.value.push_interval_seconds = data.push_interval_seconds || payload.push_interval_seconds;
                    ospfController.value.resolve_workers = data.resolve_workers || payload.resolve_workers;
                    ospfControllerDirty.value = false;
                    showToast('OSPF 推送控制已保存');
                    loadData();
                };
                const resetOspfPending = async () => {
                    if ((ospf.value?.pending || 0) <= 0) {
                        showToast('当前无 Pending Set，无需重置');
                        return;
                    }
                    customConfirm(`确认一键重置 Pending Set？\n\n将清理待发布路由（candidate/static），不影响已发布 Stable Set。`, async () => {
                        const res = await apiFetch('/api/ospf/reset_pending?confirm=APPLY', { method: 'POST' });
                        if (!res.ok) {
                            const data = await res.json().catch(() => ({}));
                            showToast(data.error || '重置 Pending Set 失败', 'error');
                            return;
                        }
                        const data = await res.json().catch(() => ({}));
                        showToast(`已重置 Pending Set，删除 ${data.deleted_pending || 0} 条待发布路由`);
                        loadData();
                    });
                };
                const sysStatus = ref({ mode: 'B', xray: false, ospf: false, mosdns: false, cpu: 0, ram: 0, interface_options: [], management_network: { iface: '', ip: '', subnet: '' }, service_network: { iface: '', ip: '', subnet: '' }, appVersion: '', osVersion: '', xrayVersion: '', frrVersion: '', commit: '', binary_build_time: '' });
                const cron = ref({ enabled: false, time: '04:00', schedule_type: 'daily', weekday: 1, monthday: 1 });
                const networkConfigForm = ref({ management_iface: '', service_iface: '' });
                const networkConfigSelecting = ref(false);
                
                const saveNetworkConfig = async () => {
                    if (!networkConfigForm.value.management_iface || !networkConfigForm.value.service_iface) {
                        showToast('请先选择管理网络和服务发布网络', 'error');
                        return;
                    }
                    const res = await apiFetch('/api/network_config?confirm=APPLY', {
                        method: 'POST',
                        body: JSON.stringify(networkConfigForm.value)
                    });
                    if (!res.ok) {
                        const d = await res.json().catch(() => ({}));
                        showToast(d.error || '网络配置保存失败', 'error');
                        return;
                    }
                    showToast('网卡角色已保存，服务发布相关配置已绑定到服务发布网络');
                    loadData(true);
                };

                const saveCron = async () => {
                    const weekday = Math.max(1, Math.min(7, parseInt(cron.value.weekday, 10) || 1));
                    const monthday = Math.max(1, Math.min(31, parseInt(cron.value.monthday, 10) || 1));
                    const payload = {
                        enabled: !!cron.value.enabled,
                        time: cron.value.time || '04:00',
                        schedule_type: cron.value.schedule_type || 'daily',
                        weekday,
                        monthday
                    };
                    const res = await apiFetch('/api/cron', { method: 'POST', body: JSON.stringify(payload) });
                    if (!res.ok) {
                        showToast("自动更新设置保存失败", 'error');
                        return;
                    }
                    const data = await res.json().catch(() => ({}));
                    cron.value.enabled = !!data.enabled;
                    cron.value.time = data.time || payload.time;
                    cron.value.schedule_type = data.schedule_type || payload.schedule_type;
                    cron.value.weekday = Math.max(1, Math.min(7, parseInt(data.weekday, 10) || weekday));
                    cron.value.monthday = Math.max(1, Math.min(31, parseInt(data.monthday, 10) || monthday));
                    showToast("自动更新设置已保存");
                };

                
                const xrayConfigStr = ref("");
                const mosdnsConfigStr = ref("");
                const nftablesConfigStr = ref("");
                const frrConfigStr = ref("");
                const currentConfigTab = ref("xray");

                const loadConfigs = async () => {
                    if (!token.value) return;
                    const load = async (name, target) => {
                        try {
                            const res = await apiFetch('/api/config/' + name);
                            if (res.ok) target.value = await res.text();
                        } catch (e) {}
                    };
                    await Promise.all([
                        load('xray', xrayConfigStr),
                        load('mosdns', mosdnsConfigStr),
                        load('nftables', nftablesConfigStr),
                        load('frr', frrConfigStr),
                    ]);
                };

                let initLoaded = false;
                
                // Remote Deploy Logic
                const isDeploying = ref(false);
                const remoteNodes = ref([]);
                const showRemoteNodeModal = ref(false);
                const showRemoteDetailsModal = ref(false);
                const selectedRemoteNode = ref(null);
                const remoteNodeForm = ref({
                    name: '', type: 'vless', ssh_host: '', ssh_port: 22, ssh_user: 'root',
                    ssh_auth_type: 'password', ssh_credential: '', region: '', remark: '',
                    port: 443, server_name: '', dest: ''
                });

                
                const showBatchModal = ref(false);
                const batchText = ref('');
                const showHistoryModal = ref(false);
                const nodeHistory = ref([]);

                const submitBatchNodes = async () => {
                    const lines = batchText.value.split('\n');
                    const reqs = [];
                    for (let line of lines) {
                        if (!line.trim()) continue;
                        const p = line.split(',');
                        if (p.length >= 7) {
                            reqs.push({
                                name: p[0], type: p[1], ssh_host: p[2], ssh_port: parseInt(p[3]),
                                ssh_user: p[4], ssh_auth_type: p[5], ssh_credential: p[6], region: p[7] || '', remark: ''
                            });
                        }
                    }
                    if (reqs.length === 0) return showToast('未解析到有效数据', 'error');

                    isDeploying.value = true;
                    try {
                        const res = await apiFetch('/api/remote_nodes/batch', {
                            method: 'POST',
                            headers: {'Content-Type': 'application/json'},
                            body: JSON.stringify(reqs)
                        });
                        const data = await res.json();
                        if(data.success) {
                            showToast('批量部署已提交，后台执行中');
                            showBatchModal.value = false;
                            batchText.value = '';
                            loadRemoteNodes();
                        } else showToast(data.error || '提交失败', 'error');
                    } catch(e) {
                        showToast('网络错误', 'error');
                    } finally { isDeploying.value = false; }
                };

                const regenerateNode = (id) => customConfirm('该操作将重新生成所有证书密钥并更换监听端口，当前使用的客户端分享链接将立刻断开失效。确认执行？', async () => {
                    const r = await apiJSON('/api/remote_nodes/' + id + '/regenerate', { method: 'POST' });
                    if (!r.ok) { showApiError(r, '重新生成任务下发失败'); if (r.status === 404) { showRemoteDetailsModal.value = false; loadRemoteNodes(); } return; }
                    showToast('重新生成任务已下发');
                    showRemoteDetailsModal.value = false;
                    loadRemoteNodes();
                });

                const setDefaultNode = async (id) => { const r = await apiJSON(`/api/nodes/${id}/default`, {method: "PUT"}); if (!r.ok) { showApiError(r, '设为默认节点失败'); if (r.status === 404) loadData(); return; } showToast("已设为默认节点"); loadData(); };
                const saveNodeFailoverMode = async () => {
                    const r = await apiJSON('/api/nodes/failover_mode', { method: 'PUT', body: JSON.stringify({ mode: nodeFailoverMode.value }) });
                    if (!r.ok) { showApiError(r, '切换故障转移模式失败'); loadNodeFailoverMode(); return; }
                    showToast(nodeFailoverMode.value === 'strict' ? '已切换为严格模式' : '已切换为普通模式');
                    loadData();
                };
                const loadNodeFailoverMode = async () => {
                    const res = await apiFetch('/api/nodes/failover_mode');
                    if (!res.ok) return;
                    const data = await res.json().catch(() => ({}));
                    nodeFailoverMode.value = (data && data.mode === 'strict') ? 'strict' : 'normal';
                };
                const loadNodeHistory = async (id) => {
                    const r = await apiJSON('/api/remote_nodes/' + id + '/history');
                    if (!r.ok) { showApiError(r, '获取历史版本失败'); if (r.status === 404) loadRemoteNodes(); return; }
                    nodeHistory.value = Array.isArray(r.data) ? r.data : [];
                    showHistoryModal.value = true;
                };

                const rollbackNode = (nodeId, historyId) => customConfirm('确定将远端服务器强行回退至该历史版本的参数和端口吗？', async () => {
                    const r = await apiJSON('/api/remote_nodes/' + nodeId + '/rollback', {
                        method: 'POST',
                        headers: {'Content-Type': 'application/json'},
                        body: JSON.stringify({ history_id: historyId })
                    });
                    if (!r.ok) { showApiError(r, '回退任务下发失败'); if (r.status === 404) { showHistoryModal.value = false; showRemoteDetailsModal.value = false; loadRemoteNodes(); } return; }
                    showToast('回退任务已下发，正在还原远端服务器');
                    showHistoryModal.value = false;
                    showRemoteDetailsModal.value = false;
                    loadRemoteNodes();
                });

                const loadRemoteNodes = async () => {
                    const r = await apiJSON('/api/remote_nodes');
                    if (r.ok) remoteNodes.value = Array.isArray(r.data) ? r.data : [];
                };

                const openAddRemoteNode = () => {
                    remoteNodeForm.value = {
                        name: '', type: 'vless', ssh_host: '', ssh_port: 22, ssh_user: 'root',
                        ssh_auth_type: 'password', ssh_credential: '', region: '', remark: '',
                        port: 443, server_name: '', dest: ''
                    };
                    showRemoteNodeModal.value = true;
                };

                const submitRemoteNode = async () => {
                    isDeploying.value = true;
                    try {
                        const res = await apiFetch('/api/remote_nodes', {
                            method: 'POST',
                            headers: {'Content-Type': 'application/json'},
                            body: JSON.stringify({
                                ...remoteNodeForm.value,
                                port: Number(remoteNodeForm.value.port) || 0
                            })
                        });
                        const data = await res.json();
                        if (data.success) {
                            showToast('节点部署任务已提交，后台部署中...');
                            showRemoteNodeModal.value = false;
                            loadRemoteNodes();
                        } else {
                            showToast(data.error || '提交失败', 'error');
                        }
                    } catch (e) {
                        showToast('网络错误', 'error');
                    } finally {
                        isDeploying.value = false;
                    }
                };

                // Deploy/check log of the node shown in the details modal
                // (GET /api/remote_nodes/:id/logs). A failed deploy used to show only
                // a "Failed" badge with no way to see why.
                const nodeLogs = ref([]);
                const nodeLogsLoading = ref(false);
                const loadNodeLogs = async (id) => {
                    nodeLogsLoading.value = true;
                    try {
                        const r = await apiJSON('/api/remote_nodes/' + id + '/logs?limit=50');
                        nodeLogs.value = (r.ok && Array.isArray(r.data.logs)) ? r.data.logs : [];
                    } finally { nodeLogsLoading.value = false; }
                };
                const prettyParams = (raw) => {
                    try { return JSON.stringify(JSON.parse(raw), null, 2); } catch (e) { return raw; }
                };
                const viewRemoteNode = async (id) => {
                    const r = await apiJSON('/api/remote_nodes/' + id);
                    if(r.ok && r.data.id) {
                        selectedRemoteNode.value = r.data;
                        nodeLogs.value = [];
                        showRemoteDetailsModal.value = true;
                        loadNodeLogs(id);
                    } else {
                        showApiError(r, '无法获取详情');
                        if (r.status === 404) loadRemoteNodes();
                    }
                };

                const checkRemoteNode = async (id) => {
                    showToast('正在执行健康检查...');
                    const r = await apiJSON('/api/remote_nodes/' + id + '/check', { method: 'POST' });
                    if(r.ok && r.data.success) {
                        showToast('节点状态正常: ' + r.data.status);
                    } else if (!r.ok) {
                        showApiError(r, '健康检查失败');
                    } else {
                        showToast('节点异常: ' + r.data.reason, 'error');
                    }
                    loadRemoteNodes();
                };

                const confirmDeleteRemoteNode = (id) => customConfirm('确定要删除此节点的部署记录和相关参数吗？(这不会卸载远端服务器上的程序)', async () => {
                    const r = await apiJSON('/api/remote_nodes/' + id, { method: 'DELETE' });
                    if (!r.ok) { showApiError(r, '删除节点记录失败'); if (r.status === 404) loadRemoteNodes(); return; }
                    showToast('节点记录已删除');
                    loadRemoteNodes();
                });

                const copyText = (text) => {
                    if (navigator.clipboard && navigator.clipboard.writeText) {
                        navigator.clipboard.writeText(text).then(() => showToast('已复制到剪贴板'));
                    } else {
                        const textArea = document.createElement("textarea");
                        textArea.value = text;
                        textArea.style.position = "fixed";
                        textArea.style.left = "-999999px";
                        textArea.style.top = "-999999px";
                        document.body.appendChild(textArea);
                        textArea.focus();
                        textArea.select();
                        try {
                            document.execCommand('copy');
                            showToast('已复制到剪贴板');
                        } catch (err) {
                            showToast('复制失败', 'error');
                        }
                        document.body.removeChild(textArea);
                    }
                };
                
                const importToLocal = async (node) => {
                    let shareLink = '';
                    if (node && node.type === 'vless' && node.vless && node.vless.share_link) {
                        shareLink = node.vless.share_link;
                    } else if (node && node.type === 'wg' && node.wg && node.wg.share_link) {
                        shareLink = node.wg.share_link;
                    }

                    if (!shareLink) {
                        showToast('导入失败：节点分享链接为空', 'error');
                        return;
                    }

                    try {
                        const res = await apiFetch('/api/nodes/import', {
                            method: 'POST',
                            body: JSON.stringify({ Url: shareLink })
                        });

                        if (res.ok) {
                            showToast('已导入至网关节点列表');
                            activeTab.value = 'nodes';
                            showRemoteDetailsModal.value = false;
                            await loadData();
                        } else {
                            let msg = '导入失败';
                            try {
                                const data = await res.json();
                                if (data && data.error) msg += '：' + data.error;
                            } catch (e) {}
                            showToast(msg, 'error');
                        }
                    } catch (e) {
                        showToast('导入失败：请求异常', 'error');
                    }
                };

                
                const formatBytes = (bytes) => {
                    if (bytes === undefined || bytes === null || isNaN(bytes)) return '0 B';
                    if (bytes === 0) return '0 B';
                    const k = 1024;
                    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
                    const i = Math.floor(Math.log(bytes) / Math.log(k));
                    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
                };

                
                const fetchConnections = async () => {
                    if (!token.value) return;
                    try {
                        const q = (connSearch.value || '').trim();
                        const url = q ? ('/api/connections?ip=' + encodeURIComponent(q)) : '/api/connections';
                        const res = await apiFetch(url); // 401 is reported by apiFetch ("登录已过期")
                        if (res.ok) {
                            const data = await res.json();
                            if (data.success && data.data) {
                                connections.value = data.data;
                            }
                        }
                    } catch (e) {}
                };

                const traceInput = ref('');
                const traceResult = ref(null);
                const isTracing = ref(false);
                const healthResults = ref([]);
                const isCheckingHealth = ref(false);
                const healthMode = ref('');

                const runTrace = async () => {
                    const target = (traceInput.value || '').trim();
                    if (!target) return showToast('请输入测试目标', 'error');
                    isTracing.value = true;
                    try {
                        const res = await apiFetch('/api/test/trace?target=' + encodeURIComponent(target));
                        if (!res.ok) throw new Error();
                        traceResult.value = await res.json();
                    } catch (e) {
                        showToast('追踪失败', 'error');
                    } finally {
                        isTracing.value = false;
                    }
                };

                const runHealthCheck = async () => {
                    isCheckingHealth.value = true;
                    try {
                        const res = await apiFetch('/api/test/health_check');
                        if (!res.ok) throw new Error();
                        const data = await res.json();
                        healthResults.value = data.results || [];
                        healthMode.value = data.mode || '';
                    } catch (e) {
                        showToast('自检失败', 'error');
                    } finally {
                        isCheckingHealth.value = false;
                    }
                };

                // Polling guard: at most one outstanding request per endpoint key, so a slow
                // backend does not accumulate duplicate requests on every 2s tick. Rejections
                // (network errors) are swallowed here; apiFetch already reported them.
                const inFlight = new Set();
                const once = (key, fn) => {
                    if (inFlight.has(key)) return;
                    inFlight.add(key);
                    Promise.resolve().then(fn).catch(() => {}).finally(() => inFlight.delete(key));
                };
                const loadData = async (isInit = false) => {
                    if (token.value && activeTab.value === 'test_tools' && !healthResults.value.length) once('health_check', runHealthCheck);
                    if (token.value && !(activeTab.value === 'system' && networkConfigSelecting.value)) once('status', () => apiJSON('/api/status').then(r => { if (!r.ok) return; const d = r.data; sysStatus.value = d; if (isInit === true) { networkConfigForm.value.management_iface = d?.management_network?.iface || ''; networkConfigForm.value.service_iface = d?.service_network?.iface || ''; } }));
                    once('traffic', fetchTraffic);
                    if (activeTab.value === 'connections') once('connections', fetchConnections);

                    if (typeof activeTab !== 'undefined' && activeTab && activeTab.value === 'syslogs') {
                        once('syslogs:' + currentLogTab.value, () => fetchSyslogs(currentLogTab.value));
                    }

                    if (token.value && (isInit === true || !initLoaded)) {
                        once('cron', () => apiJSON('/api/cron').then(r => { const d = r.data; if(r.ok && d) {
                            cron.value.enabled = !!d.enabled;
                            cron.value.time = d.time || '04:00';
                            cron.value.schedule_type = d.schedule_type || 'daily';
                            cron.value.weekday = Math.max(1, Math.min(7, parseInt(d.weekday, 10) || 1));
                            cron.value.monthday = Math.max(1, Math.min(31, parseInt(d.monthday, 10) || 1));
                        }}));
                        once('dns', () => apiJSON('/api/dns').then(r => { if (r.ok) dns.value = r.data; }));
                        initLoaded = true;
                    }
                    if(token.value && activeTab.value === 'lan_acls') once('lan_acls', () => apiJSON('/api/lan_acls').then(r => { if (!r.ok) return; lanAcls.value = r.data.acls || []; defaultLanPolicy.value = r.data.default_policy || 'proxy'; }));
                    if(token.value && activeTab.value === 'protected_ips') once('protected_ips', () => apiJSON('/api/protected_ips').then(r => { if (r.ok) protectedIps.value = r.data.items || []; }));
                    if(token.value && activeTab.value === 'nodes') {
                        once('nodes', () => apiJSON('/api/nodes').then(r => { if (r.ok) nodes.value = Array.isArray(r.data) ? r.data : []; }));
                        once('failover_mode', loadNodeFailoverMode);
                    }
                    if(token.value && activeTab.value === 'remote_nodes') once('remote_nodes', loadRemoteNodes);
                    if(token.value && (activeTab.value === 'rules' || activeTab.value === 'test_tools')) {
                        if(activeTab.value === 'rules' && !isRuleValueEditing.value && !isRuleGroupEditing.value) once('rules', loadRules);
                        if(categories.value.geosite.length===0) once('categories', () => apiJSON('/api/rules/categories').then(r => { if (r.ok) categories.value = r.data; }));
                    }
                    if(token.value && activeTab.value === 'ospf') once('ospf', () => apiJSON('/api/ospf').then(r => { if (r.ok) applyOspfPayload(r.data); }));
                    // configs: loaded when the tab becomes active (watch(activeTab)) and by its 刷新 button, not per tick.
                };

                const applyConfig = async () => {
                    if (isLoading.value.apply) return;
                    isLoading.value.apply = true;
                    isUpdating.value['apply'] = true;
                    try {
                        const r = await apiJSON('/api/apply?confirm=APPLY', {method: 'POST'});
                        if (r.ok) showToast("配置已下发，服务将平滑重启...");
                        else showApiError(r, '配置下发失败');
                    } catch (e) {
                        // network failure: apiFetch has already toasted / raised the banner
                    } finally {
                        setTimeout(() => { isUpdating.value['apply'] = false; isLoading.value.apply = false; loadData(); }, 1500);
                    }
                };

                const changeMode = async (newMode) => {
                    const descriptions = {
                        'A': 'Mode A: 全局网关劫持 (适合普通路由)',
                        'B': 'Mode B: 纯 Fake-IP 模式 (零延迟，免疫环路，OSPF只推送Fake-IP池)',
                        'C': 'Mode C: 纯 OSPF 模式 (需防环路，OSPF推送所有海外IP)'
                    };
                    const switchMode = async () => {
                        const r = await apiJSON('/api/mode?confirm=APPLY', { method: 'POST', body: JSON.stringify({ Mode: newMode }) });
                        if (!r.ok) showApiError(r, `切换失败: HTTP ${r.status}`);
                        else showToast(`已切换到 Mode ${newMode}`);
                        loadData();
                    };
                    customConfirm(`确定切换至 ${descriptions[newMode]}？`, switchMode);
                    // The select reflects the confirmed mode only; reset it until then.
                    loadData();
                }
                const toggleMode = async () => {
                    const newMode = sysStatus.value.mode === 'A' ? 'B' : 'A';
                    customConfirm(`切换到 Mode ${newMode}？ Mode A: 全局网关 (nftables) / Mode B: 旁路由纯Fake-IP (OSPF) / Mode C: 纯OSPF (恢复真实IP宣告)`, async () => {
                        const r = await apiJSON('/api/mode?confirm=APPLY', { method: 'POST', body: JSON.stringify({ Mode: newMode }) });
                        if (!r.ok) showApiError(r, `切换失败: HTTP ${r.status}`);
                        else showToast(`已切换到 Mode ${newMode}`);
                        loadData();
                    });
                }

                const importNodeUrl = async () => {
                    let url = prompt("请输入 vmess://, vless://, wireguard:// 等导入链接");
                    if(!url) return;
                    const r = await apiJSON('/api/nodes/import', { method: 'POST', body: JSON.stringify({ Url: url }) });
                    if(r.ok) { showToast("导入成功！"); loadData(); } else { showApiError(r, "导入失败，格式不支持或解析出错"); }
                }

                const pingNodes = async () => {
                    const r = await apiJSON('/api/nodes/ping', { method: 'POST' });
                    if (!r.ok) return showApiError(r, '测速任务下发失败');
                    // POST /api/nodes/ping returns { success, started, completed }; older backends only { success }.
                    if (typeof r.data.started === 'number') showToast(`已对 ${r.data.started} 个节点发起测速，稍后刷新`);
                    else showToast("TCPing 测速已在后台运行，请稍后刷新查看结果。");
                    setTimeout(loadData, 3000);
                }



                const deleteNode = (id) => { customConfirm("删除此节点？", async () => { const r = await apiJSON(`/api/nodes/${id}`, {method: 'DELETE'}); if (!r.ok) showApiError(r, '删除节点失败'); loadData(); }); };
                const toggleNode = async (id) => { const r = await apiJSON(`/api/nodes/${id}/toggle`, {method: 'PUT'}); if (!r.ok) showApiError(r, '切换节点状态失败'); loadData(); };

                const newRule = ref({ type: 'domain', value: '', action: 'proxy', primary: '', standby: '', policy: 'proxy', group_name: '' });
                const wildcardDomainRulePattern = /^(?:\*\*|\*)\.[a-z0-9.-]+$/i;
                const shortRuleGroup = (groupId) => groupId ? groupId.slice(0, 12) : '—';
                const isRuleGroupEditing = ref(false);
                const syncEditingRuleGroup = () => {
                    if (isRuleGroupEditing.value) return;
                    const current = ruleGroups.value.find(group => group.group_id === selectedRuleGroup.value);
                    editingRuleGroup.value = current ? (current.group_name || '') : '';
                };
                const handleRuleGroupFocus = () => { isRuleGroupEditing.value = true; };
                const handleRuleGroupBlur = () => {
                    setTimeout(() => {
                        isRuleGroupEditing.value = false;
                        syncEditingRuleGroup();
                    }, 120);
                };
                const loadRules = async () => {
                    const suffix = selectedRuleGroup.value ? ('?group_id=' + encodeURIComponent(selectedRuleGroup.value)) : '';
                    const res = await apiFetch('/api/rules' + suffix);
                    if (!res.ok) {
                        const data = await res.json().catch(() => ({}));
                        showToast(data.error || '加载规则失败', 'error');
                        return;
                    }
                    const data = await res.json().catch(() => ({}));
                    rules.value = Array.isArray(data.rules) ? data.rules : [];
                    ruleGroups.value = Array.isArray(data.groups) ? data.groups : [];
                    if (selectedRuleGroup.value && !ruleGroups.value.some(group => group.group_id === selectedRuleGroup.value)) {
                        selectedRuleGroup.value = '';
                    }
                    syncEditingRuleGroup();
                };
                const filteredRules = computed(() => {
                    if (!ruleSearchId.value) return rules.value;
                    return rules.value.filter(rule => String(rule.id) === String(ruleSearchId.value));
                });
                const goToRule = (ruleId) => {
                    if (!ruleId) return;
                    activeTab.value = 'rules';
                    selectedRuleGroup.value = '';
                    ruleSearchId.value = String(ruleId);
                    loadRules();
                };
                const describeRulePattern = (rule) => {
                    const value = ((rule && rule.value) || '').trim().toLowerCase().replace(/\.+$/, '');
                    if (!value) return '—';
                    if (value.startsWith('*.')) return '根域 + 0/1 层子域';
                    if (value.startsWith('**.')) return '根域 + 任意层子域';
                    return '仅根域';
                };
                const ruleValuePlaceholder = computed(() => {
                    if (newRule.value.type === 'domain') return '如 c.com（仅根域）、**.c.com、*.c.com';
                    if (newRule.value.type === 'geoip') return '如 cn、!cn';
                    if (newRule.value.type === 'geosite') return '如 google、gfw';
                    return '如 8.8.8.8/32';
                });
                const ruleValidationError = computed(() => {
                    const value = (newRule.value.value || '').trim();
                    if (!value) return '';

                    if (newRule.value.type === 'domain') {
                        const parts = value.split(/[，,]+|\s+/).map(item => item.trim()).filter(Boolean);

                        for (const item of parts) {
                            const normalized = item.replace(/\.+$/, '').toLowerCase();
                            if (normalized.includes('*') && !wildcardDomainRulePattern.test(normalized)) {
                                return '域名 wildcard 仅支持 *.example.com 或 **.example.com';
                            }
                        }
                    } else if (newRule.value.type === 'geoip' || newRule.value.type === 'geolocation') {
                        const tag = value.toLowerCase();
                        if (tag !== 'private') {
                            const tags = (categories.value.geoip || []).map(t => String(t).toLowerCase());
                            if (!tags.includes(tag)) {
                                return '无效 GeoIP 标签: ' + value;
                            }
                        }
                    } else if (newRule.value.type === 'geosite') {
                        const tag = value.toLowerCase();
                        const tags = (categories.value.geosite || []).map(t => String(t).toLowerCase());
                        if (!tags.includes(tag)) {
                            return '无效 Geosite 标签: ' + value;
                        }
                    } else if (newRule.value.type === 'ip') {
                        const ipOrCidr = /^(?:\d{1,3}\.){3}\d{1,3}(?:\/(?:[0-9]|[1-2]\d|3[0-2]))?$/;
                        if (!ipOrCidr.test(value)) {
                            return 'IP/CIDR 格式无效（示例：8.8.8.8 或 8.8.8.0/24）';
                        }
                    }

                    if (newRule.value.action === 'node') {
                        if (!newRule.value.primary) return '请选择指定节点';
                        const exists = (nodes.value || []).some(n => String(n.id) === String(newRule.value.primary));
                        if (!exists) return '所选节点不存在或已被删除';
                    } else if (newRule.value.action === 'ha') {
                        if (!newRule.value.primary || !newRule.value.standby) return '请选择主备节点';
                        if (newRule.value.primary === newRule.value.standby) return '主备节点不能相同';
                        const hasPrimary = (nodes.value || []).some(n => String(n.id) === String(newRule.value.primary));
                        const hasStandby = (nodes.value || []).some(n => String(n.id) === String(newRule.value.standby));
                        if (!hasPrimary || !hasStandby) return '主备节点不存在或已被删除';
                    }

                    return '';
                });
                const isRuleValueEditing = ref(false);
                const handleRuleValueFocus = () => { isRuleValueEditing.value = true; };
                const handleRuleValueBlur = () => {
                    setTimeout(() => { isRuleValueEditing.value = false; }, 120);
                };
                const addRule = async () => {
                    const trimmedValue = (newRule.value.value || '').trim();
                    if(!trimmedValue) return showToast("匹配值不能为空", 'error');
                    if(ruleValidationError.value) return showToast(ruleValidationError.value, 'error');
                    let finalPolicy = newRule.value.action;
                    if(finalPolicy === 'node') {
                        if(!newRule.value.primary) return showToast("请选择指定节点", 'error');
                        finalPolicy = 'proxy-' + newRule.value.primary;
                    } else if(finalPolicy === 'ha') {
                        if(!newRule.value.primary || !newRule.value.standby) return showToast("请选择主备节点", 'error');
                        if(newRule.value.primary === newRule.value.standby) return showToast("主备节点不能相同", 'error');
                        finalPolicy = 'ha-' + newRule.value.primary + '-' + newRule.value.standby;
                    }
                    const res = await apiFetch('/api/rules', { method: 'POST', body: JSON.stringify({ Type: newRule.value.type, Value: trimmedValue, Policy: finalPolicy, GroupName: newRule.value.group_name || '' }) });
                    if(!res.ok) {
                        const data = await res.json().catch(() => ({}));
                        return showToast(data.error || `添加失败: HTTP ${res.status}`, 'error');
                    }
                    const data = await res.json().catch(() => ({}));
                    newRule.value.value = '';
                    newRule.value.group_name = '';
                    if ((data.count || 0) > 1 && data.group_id) {
                        showToast(`已批量添加 ${data.count} 条规则，并自动分组`, 'success');
                    } else {
                        showToast('规则已添加');
                    }
                    loadData();
                };
                const moveRule = async (ruleId, direction) => {
                    if (ruleSearchId.value) return showToast('已定位单条规则，请先清除定位再调整顺序', 'error');
                    const list = Array.isArray(filteredRules.value) ? [...filteredRules.value] : [];
                    const idx = list.findIndex(rule => String(rule.id) === String(ruleId));
                    if (idx < 0) return;
                    const swapIdx = idx + direction;
                    if (swapIdx < 0 || swapIdx >= list.length) return;

                    const ids = list.map(rule => rule.id);
                    [ids[idx], ids[swapIdx]] = [ids[swapIdx], ids[idx]];

                    const res = await apiFetch('/api/rules/reorder', { method: 'PUT', body: JSON.stringify({ ids }) });
                    if (!res.ok) {
                        const data = await res.json().catch(() => ({}));
                        return showToast(data.error || '规则重排失败', 'error');
                    }
                    showToast('规则优先级已更新');
                    await loadRules();
                };
                const deleteRule = (id) => { customConfirm("删除此分流规则？", async () => { const r = await apiJSON(`/api/rules/${id}`, {method: 'DELETE'}); if (!r.ok) showApiError(r, '删除规则失败'); loadRules(); }); };
                const deleteRuleGroup = (groupId) => { customConfirm(`删除分组 ${shortRuleGroup(groupId)} 下全部规则？`, async () => { const res = await apiFetch(`/api/rules/group/${groupId}`, {method: 'DELETE'}); if(!res.ok) { const data = await res.json().catch(() => ({})); showToast(data.error || '删组失败', 'error'); return; } showToast('分组规则已删除'); if (selectedRuleGroup.value === groupId) selectedRuleGroup.value = ''; loadRules(); }); };
                const saveRuleGroupName = async () => { if (!selectedRuleGroup.value) return; const res = await apiFetch(`/api/rules/group/${selectedRuleGroup.value}`, { method: 'PUT', body: JSON.stringify({ group_name: editingRuleGroup.value || '' }) }); if(!res.ok) { const data = await res.json().catch(() => ({})); return showToast(data.error || '保存组备注失败', 'error'); } showToast('组备注已保存'); loadRules(); };

                const saveDns = async () => { const r = await apiJSON('/api/dns', { method: 'POST', body: JSON.stringify({ Local: dns.value.local, Remote: dns.value.remote, Lazy: dns.value.lazy, Mode: dns.value.mode, log_level: dns.value.log_level, cache_size: parseInt(dns.value.cache_size, 10), lazy_ttl: parseInt(dns.value.lazy_ttl, 10) }) }); if (!r.ok) return showApiError(r, 'DNS 配置保存失败'); showToast("DNS 配置已保存！"); loadData(); }
                const resetDns = () => {
                    dns.value = {
                        local: '119.29.29.29,223.5.5.5',
                        remote: '1.1.1.1,8.8.8.8',
                        lazy: true,
                        mode: 'smart',
                        log_level: 'info',
                        cache_size: 10240,
                        lazy_ttl: 86400
                    };
                    showToast("已重置为默认值（尚未保存）");
                };
                const updateComp = async (comp) => { 
                    customConfirm(`确认执行此操作？这可能需要几十秒时间下载更新。`, async () => {
                        isUpdating.value[comp] = true;
                        showToast("任务已触发，后台下载中...", "success");
                        try {
                            const res = await apiFetch(`/api/update/${comp}`, { method: 'POST' });
                            if(res.ok) {
                                showToast("操作成功完成！");
                            } else {
                                try {
                                    const data = await res.json();
                                    showToast("更新失败: " + (data.error || "未知错误"), "error");
                                } catch(e) {
                                    showToast("更新失败，请检查网络", "error");
                                }
                            }
                        } catch (e) {
                            showToast("请求出错", "error");
                        }
                        isUpdating.value[comp] = false;
                        loadData();
                    }); 
                }

                let pollTimer = null;
                const startVisiblePolling = () => {
                    if (pollTimer !== null) return;
                    pollTimer = setInterval(() => {
                        if (document.visibilityState !== 'visible') return;
                        loadData(false);
                    }, 2000);
                };
                const stopVisiblePolling = () => {
                    if (pollTimer === null) return;
                    clearInterval(pollTimer);
                    pollTimer = null;
                };
                const handleVisibilityPolling = () => {
                    if (document.visibilityState === 'visible') {
                        loadData(false);
                        startVisiblePolling();
                    } else {
                        stopVisiblePolling();
                    }
                };

                onMounted(() => {
                    loadData(true);
                    if (activeTab.value === 'configs') loadConfigs();
                    document.addEventListener('visibilitychange', handleVisibilityPolling);
                    if (document.visibilityState === 'visible') startVisiblePolling();
                });

                                return { lockScroll, scrollToBottom, nextTick, syslogs, currentLogTab, syslogsContainer, fetchSyslogs, traffic, formatBytes,
                    connections, connSearch, filteredConnections, lanAcls, defaultLanPolicy, protectedIps, protectedIpForm, goToRule,
                    showAclModal, aclForm, openAddAcl, saveAcl, deleteAcl, updateLanDefault, addProtectedIp, deleteProtectedIp,
                    currentConfigTab, backendDown, logLines, logSince, nodeLogs, nodeLogsLoading, loadNodeLogs, prettyParams,
                    loadData,
                    isSubmitting, isLoading, vlessParams, cron, saveCron, networkConfigForm, networkConfigSelecting, saveNetworkConfig, showPwdModal, pwdForm, logout, changePwd, toastMsg, toastType, confirmData, doConfirm, isUpdating, token, password, login, categories, geoQuery, showGeoSuggestions, geoSuggestionIndex, filteredGeoSuggestions, handleGeoInput, handleGeoInputBlur, moveGeoSuggestion, confirmGeoSuggestionOrQuery, applyGeoSuggestion, runGeoQuery, xrayConfigStr, mosdnsConfigStr, nftablesConfigStr, frrConfigStr, loadConfigs, newRule, ruleGroups, selectedRuleGroup, ruleSearchId, filteredRules, editingRuleGroup, shortRuleGroup, describeRulePattern, ruleValuePlaceholder, ruleValidationError, handleRuleValueFocus, handleRuleValueBlur, addRule, loadRules, saveRuleGroupName, moveRule, activeTab, tabs, visibleTabs, uiMode, toggleUiMode, currentTabLabel, nodes, rules, dns, dnsLogs, ospf, ospfController, ospfControllerDirty, saveOspfController, resetOspfPending, sysStatus, applyConfig, toggleMode, changeMode, importNodeUrl, pingNodes, openAddNode, editNode, saveNode, showNodeModal, editingNode, deleteNode, toggleNode, deleteRule, deleteRuleGroup, saveDns, resetDns, updateComp, showMosdnsRollbackModal, mosdnsVersions, selectedMosdnsRollbackVersion, openMosdnsRollbackModal, confirmMosdnsRollback, showRollbackModal, xrayVersions, selectedRollbackVersion, openRollbackModal, confirmRollback, remoteNodes, showRemoteNodeModal, showRemoteDetailsModal, selectedRemoteNode, remoteNodeForm, loadRemoteNodes, openAddRemoteNode, submitRemoteNode, viewRemoteNode, checkRemoteNode, confirmDeleteRemoteNode, copyText, importToLocal, isDeploying, showBatchModal, batchText, submitBatchNodes, showHistoryModal, nodeHistory,                    setDefaultNode, nodeFailoverMode, saveNodeFailoverMode, regenerateNode, loadNodeHistory, rollbackNode,
                    traceInput, traceResult, isTracing, runTrace, healthResults, isCheckingHealth, runHealthCheck, healthMode }
            }
});
app.config.errorHandler = (err, instance, info) => { reportError('组件错误: ' + (err && err.message ? err.message : String(err)) + (info ? ' (' + info + ')' : '')); };
app.mount('#app');
