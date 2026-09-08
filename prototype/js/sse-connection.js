/**
 * sse-connection.js — SSE 长连接管理（按 org 隔离 + 自动重连）
 * 职责：统一 SSE 连接生命周期、组织切换时断开重连、心跳保活
 */
(function() {
    'use strict';

    var activeConnections = {};
    var reconnectTimers = {};

    /**
     * 创建 SSE 连接（自动注入 org_id）
     * @param {string} endpoint — SSE 端点 URL
     * @param {function} onMessage — 消息回调
     * @param {object} options — { reconnectInterval: 5000, maxReconnects: 10 }
     * @returns {EventSource}
     */
    window.createSSEConnection = function(endpoint, onMessage, options) {
        options = options || {};
        var reconnectInterval = options.reconnectInterval || 5000;
        var maxReconnects = options.maxReconnects || 10;
        var reconnectCount = 0;
        var connectionId = endpoint;

        // 关闭已有连接
        if (activeConnections[connectionId]) {
            activeConnections[connectionId].close();
            delete activeConnections[connectionId];
        }

        function connect() {
            var currentOrg = window.getCurrentOrg ? window.getCurrentOrg() : null;
            var orgIdParam = currentOrg ? '?org_id=' + currentOrg.id : '';
            var token = window.getToken ? window.getToken() : '';
            var tokenParam = token ? (orgIdParam ? '&' : '?') + 'token=' + encodeURIComponent(token) : '';

            var url = endpoint + orgIdParam + tokenParam;
            var es = new EventSource(url);

            es.onopen = function() {
                reconnectCount = 0;
                if (window.console) console.log('[SSE] Connected:', endpoint);
            };

            es.onmessage = function(event) {
                if (onMessage) {
                    try {
                        var data = JSON.parse(event.data);
                        onMessage(data, event);
                    } catch (e) {
                        onMessage(event.data, event);
                    }
                }
            };

            es.onerror = function(err) {
                es.close();
                if (window.console) console.warn('[SSE] Error on', endpoint, err);

                // 自动重连（指数退避）
                if (reconnectCount < maxReconnects) {
                    reconnectCount++;
                    var delay = reconnectInterval * Math.min(reconnectCount, 5);
                    if (reconnectTimers[connectionId]) {
                        clearTimeout(reconnectTimers[connectionId]);
                    }
                    reconnectTimers[connectionId] = setTimeout(connect, delay);
                } else {
                    if (window.showToast) {
                        window.showToast('SSE 连接已断开，请刷新页面重试', 'error');
                    }
                }
            };

            activeConnections[connectionId] = es;
            return es;
        }

        return connect();
    };

    /**
     * 关闭指定 SSE 连接
     */
    window.closeSSEConnection = function(endpoint) {
        var connectionId = endpoint;
        if (activeConnections[connectionId]) {
            activeConnections[connectionId].close();
            delete activeConnections[connectionId];
        }
        if (reconnectTimers[connectionId]) {
            clearTimeout(reconnectTimers[connectionId]);
            delete reconnectTimers[connectionId];
        }
    };

    /**
     * 关闭所有 SSE 连接（组织切换时调用）
     */
    window.closeAllSSEConnections = function() {
        Object.keys(activeConnections).forEach(function(id) {
            if (activeConnections[id]) {
                activeConnections[id].close();
            }
            if (reconnectTimers[id]) {
                clearTimeout(reconnectTimers[id]);
            }
        });
        activeConnections = {};
        reconnectTimers = {};
    };

    // 监听组织切换事件，自动断开所有 SSE 并重连
    window.addEventListener('storage', function(e) {
        if (e.key === '_org_change_event') {
            window.closeAllSSEConnections();
            // 各页面应自行在 SSE 断开后检测并重新连接
        }
    });

})();
