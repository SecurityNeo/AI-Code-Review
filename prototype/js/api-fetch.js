/**
 * api-fetch.js — 统一 API 请求封装
 * 职责：自动注入 X-Org-ID、Token、指数退避重试、AbortController
 */
(function () {
    'use strict';

    const DEFAULT_TIMEOUT = 30000;
    const MAX_RETRIES = 3;
    const RETRY_DELAY_BASE = 1000;

    /**
     * 合并两个 AbortSignal（用户传入 + 内部超时）
     */
    function combineSignals(userSignal, internalSignal) {
        if (!userSignal) return internalSignal;
        const ctrl = new AbortController();
        function onAbort() { ctrl.abort(); }
        userSignal.addEventListener('abort', onAbort);
        internalSignal.addEventListener('abort', onAbort);
        return ctrl.signal;
    }

    /**
     * 带自动注入 org_id 和 token 的 fetch 封装
     * @param {string} url
     * @param {object} options
     * @returns {Promise<Response>}
     */
    window.apiFetch = async function apiFetch(url, options) {
        options = options || {};

        // 重试逻辑
        let lastError;
        for (let attempt = 0; attempt < MAX_RETRIES; attempt++) {
            const headers = Object.assign({}, options.headers || {});

            // 注入认证 Token
            const token = window.getToken ? window.getToken() : null;
            if (token && !headers['Authorization']) {
                headers['Authorization'] = 'Bearer ' + token;
            }

            // 多租户改造：自动注入 X-Org-ID
            const currentOrg = window.getCurrentOrg ? window.getCurrentOrg() : null;
            if (currentOrg && currentOrg.id && !headers['X-Org-Id'] && !headers['x-org-id']) {
                headers['X-Org-Id'] = String(currentOrg.id);
            }

            // body 自动 JSON.stringify（普通对象 / 数组）
            let body = options.body;
            if (body && typeof body === 'object' && !(body instanceof FormData) && !(body instanceof Blob) && !(body instanceof ArrayBuffer) && !(body instanceof URLSearchParams) && !(body instanceof ReadableStream)) {
                body = JSON.stringify(body);
                if (!headers['Content-Type'] && !headers['content-type']) {
                    headers['Content-Type'] = 'application/json';
                }
            }

            const fetchOptions = Object.assign({}, options, { headers, body });

            // AbortController（每次重试重新创建，避免上一次超时信号影响）
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), options.timeout || DEFAULT_TIMEOUT);

            // 合并用户 signal 和内部超时 signal
            fetchOptions.signal = combineSignals(options.signal, controller.signal);

            try {
                const res = await fetch(url, fetchOptions);
                clearTimeout(timeoutId);

                // 统一错误码处理
                if (res.status === 403) {
                    const data = await res.clone().json().catch(() => ({}));
                    if (data.error && data.error.includes('未分配组织')) {
                        window.location.href = '/pending-assignment.html';
                        return res;
                    }
                }
                if (res.status === 401) {
                    if (window.redirectToLogin) {
                        window.redirectToLogin();
                    }
                    return res;
                }

                // 非 2xx 响应统一抛错，避免调用方静默失败
                if (!res.ok) {
                    const data = await res.clone().json().catch(() => ({}));
                    const err = new Error(data.error || ('HTTP ' + res.status));
                    err.status = res.status;
                    err.data = data;
                    throw err;
                }

                return res;
            } catch (err) {
                clearTimeout(timeoutId);
                lastError = err;

                if (err.name === 'AbortError') {
                    throw new Error('请求超时');
                }

                // 网络抖动：指数退避重试
                if (attempt < MAX_RETRIES - 1) {
                    const delay = RETRY_DELAY_BASE * Math.pow(2, attempt);
                    await new Promise(r => setTimeout(r, delay));
                }
            }
        }

        throw lastError || new Error('请求失败');
    };

    window.apiGet = function (url, options) {
        return window.apiFetch(url, Object.assign({ method: 'GET' }, options));
    };
    window.apiPost = function (url, body, options) {
        return window.apiFetch(url, Object.assign({
            method: 'POST',
            body: typeof body === 'string' ? body : JSON.stringify(body)
        }, options));
    };
    window.apiPut = function (url, body, options) {
        return window.apiFetch(url, Object.assign({
            method: 'PUT',
            body: typeof body === 'string' ? body : JSON.stringify(body)
        }, options));
    };
    window.apiDelete = function (url, options) {
        return window.apiFetch(url, Object.assign({ method: 'DELETE' }, options));
    };
})();
