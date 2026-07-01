import React, { createContext, useContext, useEffect, useRef, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import * as api from './api'

const AuthContext = createContext(null)

// 认证上下文：基于HttpOnly Cookie，token不再存在前端
export function AuthProvider({ children }) {
  const [username, setUsername] = useState('')
  const [role, setRole] = useState('')
  const [user, setUser] = useState(null)
  const [loading, setLoading] = useState(true)
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  // 登录成功后更新前端状态(Cookie已由服务端Set-Cookie设置)
  function handleLogin(data) {
    const { username: u, role: r } = data
    localStorage.setItem('fci_username', u)
    if (r) {
      localStorage.setItem('fci_role', r)
    } else {
      localStorage.removeItem('fci_role')
    }
    setUsername(u)
    setRole(r || '')
    setIsAuthenticated(true)
    setUser({ id: data.id, username: u, role: r })
  }

  // 登出：调用服务端清除Cookie，清空本地状态
  async function handleLogout() {
    try {
      await api.logout()
    } catch (e) {
      // 忽略网络错误，本地状态仍需清空
    }
    localStorage.removeItem('fci_username')
    localStorage.removeItem('fci_role')
    if (mountedRef.current) {
      setUsername('')
      setRole('')
      setUser(null)
      setIsAuthenticated(false)
    }
  }

  // 拉取当前用户信息(页面加载时用于恢复登录态)
  async function refreshUser() {
    try {
      const res = await api.getUser()
      if (!mountedRef.current) return null
      if (res?.success && res.data) {
        setUser(res.data)
        setUsername(res.data.username || '')
        setRole(res.data.role || '')
        setIsAuthenticated(true)
        if (res.data.username) {
          localStorage.setItem('fci_username', res.data.username)
        }
        if (res.data.role) {
          localStorage.setItem('fci_role', res.data.role)
        }
        return res.data
      }
    } catch (e) {
      if (!mountedRef.current) return null
      // 401说明Cookie无效/过期，清空登录态
      if (e?.response?.status === 401) {
        localStorage.removeItem('fci_username')
        localStorage.removeItem('fci_role')
        setUsername('')
        setRole('')
        setUser(null)
        setIsAuthenticated(false)
      }
    } finally {
      if (mountedRef.current) {
        setLoading(false)
      }
    }
    return null
  }

  // 页面加载时尝试通过Cookie恢复登录态
  useEffect(() => {
    refreshUser()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const value = {
    username,
    role,
    user,
    loading,
    login: handleLogin,
    logout: handleLogout,
    refreshUser,
    isAuthenticated,
    isAdmin: role === 'admin',
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth 必须在 AuthProvider 内使用')
  }
  return ctx
}

// 受保护路由：加载中显示空白，未登录跳登录页
export function ProtectedRoute({ children }) {
  const { isAuthenticated, loading } = useAuth()
  const location = useLocation()
  if (loading) {
    return null
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  return children
}

// 管理员路由：非admin跳控制台
export function AdminRoute({ children }) {
  const { isAdmin, loading } = useAuth()
  if (loading) {
    return null
  }
  if (!isAdmin) {
    return <Navigate to="/console" replace />
  }
  return children
}
