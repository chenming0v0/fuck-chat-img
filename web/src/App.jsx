import React from 'react'
import { Routes, Route, Navigate, Link } from 'react-router-dom'
import { AuthProvider, ProtectedRoute, AdminRoute } from '@/helpers/auth'
import { ThemeProvider } from '@/context/Theme'
import PageLayout from '@/components/layout/PageLayout'
import Login from '@/pages/Login'
import Setup from '@/pages/Setup'
import Dashboard from '@/pages/Dashboard'
import ModelGroup from '@/pages/ModelGroup'
import ModelPlaza from '@/pages/ModelPlaza'
import History from '@/pages/History'
import Settings from '@/pages/Settings'

// 404 页：简单居中文字
function NotFound() {
  return (
    <div className="flex flex-col items-center justify-center min-h-screen gap-2">
      <div className="text-6xl font-bold tracking-tight">404</div>
      <div className="text-sm text-semi-color-text-2">
        页面不存在
      </div>
      {/* SPA 内部用 Link 走客户端路由, 避免整页刷新丢状态 */}
      <Link
        to="/console"
        className="mt-2 text-sm !rounded-full px-4 py-2"
        style={{
          background: 'var(--semi-color-primary)',
          color: '#fff',
        }}
      >
        返回控制台
      </Link>
    </div>
  )
}

export default function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/setup" element={<Setup />} />
          <Route path="/" element={<Navigate to="/console" replace />} />
          <Route
            path="/console"
            element={
              <ProtectedRoute>
                <PageLayout />
              </ProtectedRoute>
            }
          >
            <Route index element={<Dashboard />} />
            <Route path="groups" element={<AdminRoute><ModelGroup /></AdminRoute>} />
            <Route path="plaza" element={<ModelPlaza />} />
            <Route path="history" element={<AdminRoute><History /></AdminRoute>} />
            <Route path="settings" element={<Settings />} />
          </Route>
          <Route path="*" element={<NotFound />} />
        </Routes>
      </AuthProvider>
    </ThemeProvider>
  )
}
