import React, { createContext, useContext, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import * as api from './api'

const AuthContext = createContext(null)

// 提供 token、username、role，以及 login/logout 方法
export function AuthProvider({ children }) {
  const [token, setToken] = useState(() => localStorage.getItem('fci_token'))
  const [username, setUsername] = useState(
    () => localStorage.getItem('fci_username') || '',
  )
  const [role, setRole] = useState(() => localStorage.getItem('fci_role') || '')
  const [user, setUser] = useState(null)

  // 登录：保存 token、用户名、角色
  function handleLogin(data) {
    const { token: t, username: u, role: r } = data
    localStorage.setItem('fci_token', t)
    localStorage.setItem('fci_username', u)
    if (r) localStorage.setItem('fci_role', r)
    setToken(t)
    setUsername(u)
    setRole(r || '')
  }

  // 登出：清空本地存储与状态
  function logout() {
    localStorage.removeItem('fci_token')
    localStorage.removeItem('fci_username')
    localStorage.removeItem('fci_role')
    setToken(null)
    setUsername('')
    setRole('')
    setUser(null)
  }

  // 拉取当前用户信息（用于刷新页面后恢复 role）
  async function refreshUser() {
    if (!token) return null
    try {
      const res = await api.getUser()
      if (res?.success) {
        setUser(res.data)
        if (res.data?.role) {
          setRole(res.data.role)
          localStorage.setItem('fci_role', res.data.role)
        }
        return res.data
      }
    } catch (e) {
      // token 失效则登出
      if (e?.response?.status === 401) {
        logout()
      }
    }
    return null
  }

  useEffect(() => {
    if (token) {
      refreshUser()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token])

  const value = {
    token,
    username,
    role,
    user,
    login: handleLogin,
    logout,
    refreshUser,
    isAuthenticated: !!token,
    isAdmin: role === 'admin',
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// 获取鉴权上下文
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth 必须在 AuthProvider 内使用')
  }
  return ctx
}

// 受保护路由：无 token 跳登录
export function ProtectedRoute({ children }) {
  const { isAuthenticated } = useAuth()
  const location = useLocation()
  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  return children
}

// 管理员路由：非 admin 跳控制台
export function AdminRoute({ children }) {
  const { isAdmin } = useAuth()
  if (!isAdmin) {
    return <Navigate to="/console" replace />
  }
  return children
}
