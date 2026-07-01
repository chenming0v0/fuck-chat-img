import axios from 'axios'

let isHandling401 = false

// 统一的 axios 实例，baseURL 同源 /api
const instance = axios.create({
  baseURL: '/api',
  timeout: 30000,
  withCredentials: true,
})

// 响应拦截器：401 时清空登录态并跳转登录页
instance.interceptors.response.use(
  (response) => response.data,
  (error) => {
    const status = error?.response?.status
    if (status === 401) {
      if (isHandling401) {
        return Promise.reject(error)
      }
      isHandling401 = true
      localStorage.removeItem('fci_username')
      localStorage.removeItem('fci_role')
      // 调用服务端登出清除Cookie
      instance.post('/logout').catch(() => {})
      // 仅在浏览器环境跳转，避免刷新覆盖
      if (typeof window !== 'undefined' && window.location.pathname !== '/login' && window.location.pathname !== '/setup') {
        window.location.href = '/login'
      }
      setTimeout(() => {
        isHandling401 = false
      }, 1000)
    }
    return Promise.reject(error)
  },
)

// 简化的错误处理：返回后端 message 或默认文本
function pickMessage(error, fallback = '请求失败') {
  return (
    error?.response?.data?.message ||
    error?.response?.data?.error?.message ||
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
  return instance.post('/login', payload)
}

// 2.0 登出
export function logout() {
  return instance.post('/logout')
}

// 2.1 首次设置管理员账户(仅在无任何用户时可用)
export function setup(payload) {
  return instance.post('/setup', payload)
}

// 3. 获取当前用户
export function getUser() {
  return instance.get('/user')
}

// 4. 修改密码
export function changePassword(payload) {
  return instance.post('/user/password', payload)
}

// 5. 模型组
export function listGroups(params = {}) {
  return instance.get('/groups', { params })
}
export function getGroup(id) {
  return instance.get(`/groups/${id}`)
}
export function getGroupPlain(id) {
  return instance.get(`/groups/${id}/plain`)
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
