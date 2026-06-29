import axios from 'axios'

// 统一的 axios 实例，baseURL 同源 /api
const instance = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

// 请求拦截器：注入 Authorization 头
instance.interceptors.request.use(
  (config) => {
    const token = localStorage.getItem('fci_token')
    if (token) {
      config.headers = config.headers || {}
      config.headers.Authorization = `Bearer ${token}`
    }
    return config
  },
  (error) => Promise.reject(error),
)

// 响应拦截器：401 时清空 token 并跳转登录页
instance.interceptors.response.use(
  (response) => response.data,
  (error) => {
    const status = error?.response?.status
    if (status === 401) {
      localStorage.removeItem('fci_token')
      localStorage.removeItem('fci_username')
      // 仅在浏览器环境跳转，避免刷新覆盖
      if (typeof window !== 'undefined' && window.location.pathname !== '/login') {
        window.location.href = '/login'
      }
    }
    return Promise.reject(error)
  },
)

// 简化的错误处理：返回后端 message 或默认文本
function pickMessage(error, fallback = '请求失败') {
  return (
    error?.response?.data?.message ||
    error?.response?.data?.error ||
    error?.message ||
    fallback
  )
}

// ===== 各业务 API =====

// 1. 服务状态
export function getStatus() {
  return instance.get('/status')
}

// 2. 登录
export function login(payload) {
  // payload: { username, password }
  return instance.post('/login', payload)
}

// 3. 获取当前用户
export function getUser() {
  return instance.get('/user')
}

// 4. 修改密码
export function changePassword(payload) {
  // payload: { old_password, new_password }
  return instance.post('/user/password', payload)
}

// 5. 模型组
export function listGroups(params = {}) {
  // params: { p, size, keyword }
  return instance.get('/groups', { params })
}
export function getGroup(id) {
  return instance.get(`/groups/${id}`)
}
export function createGroup(payload) {
  return instance.post('/groups', payload)
}
export function updateGroup(id, payload) {
  return instance.put(`/groups/${id}`, payload)
}
export function deleteGroup(id) {
  return instance.delete(`/groups/${id}`)
}
export function toggleGroup(id) {
  return instance.post(`/groups/${id}/toggle`)
}
export function testGroup(id) {
  return instance.get(`/groups/${id}/test`)
}

// 6. 历史
export function listHistory(params = {}) {
  // params: { p, size, keyword, group, success, cache_hit }
  return instance.get('/history', { params })
}
export function getHistory(id) {
  return instance.get(`/history/${id}`)
}
export function deleteHistory(id) {
  return instance.delete(`/history/${id}`)
}
export function clearHistory() {
  return instance.delete('/history')
}
export function historyStats() {
  return instance.get('/history/stats')
}

// 7. 缓存
export function cacheStats() {
  return instance.get('/cache/stats')
}
export function cacheClear() {
  return instance.delete('/cache')
}

export { pickMessage }
export default instance
