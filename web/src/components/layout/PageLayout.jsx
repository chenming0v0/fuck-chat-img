import React, { useEffect, useState } from 'react'
import { Layout } from '@douyinfe/semi-ui'
import { Outlet } from 'react-router-dom'
import HeaderBar from './HeaderBar'
import SiderBar from './SiderBar'

const { Header, Sider, Content } = Layout

// 三段式固定布局：Header + Sider + Content
// 提示统一用 Semi UI Toast(自带挂载容器), 无需在此处放 ToastContainer.
export default function PageLayout() {
  const [collapsed, setCollapsed] = useState(false)

  // 用 useEffect 同步 body class 并在卸载时清理: 避免跳转到 /login 等非 console
  // 路由后 body 仍残留 sidebar-collapsed 类, 导致下次进入控制台时样式错位.
  useEffect(() => {
    if (typeof document !== 'undefined') {
      document.body.classList.toggle('sidebar-collapsed', collapsed)
    }
    return () => {
      if (typeof document !== 'undefined') {
        document.body.classList.remove('sidebar-collapsed')
      }
    }
  }, [collapsed])

  function toggleCollapsed() {
    setCollapsed((prev) => !prev)
  }

  return (
    <Layout className="classic-page-fill app-layout-fixed">
      {/* 顶部毛玻璃 Header */}
      <Header
        className="fixed top-0 left-0 right-0 z-50 !h-16 !bg-white/75 dark:!bg-zinc-900/75 backdrop-blur-lg"
        style={{ padding: 0, borderBottom: '1px solid var(--semi-color-border)' }}
      >
        <HeaderBar />
      </Header>

      {/* 侧边栏：移动端隐藏 */}
      <Sider
        className="fixed left-0 z-40 !bg-semi-color-bg-1 scrollbar-hide"
        style={{
          top: '64px',
          bottom: 0,
          width: 'var(--sidebar-current-width)',
          borderRight: '1px solid var(--semi-color-border)',
        }}
      >
        <SiderBar collapsed={collapsed} onToggle={toggleCollapsed} />
      </Sider>

      {/* 内容区：顶部留 64px，左侧让位侧边栏 */}
      <Content
        className="classic-page-fill"
        style={{ paddingTop: '64px', minHeight: '100vh' }}
      >
        <div style={{ padding: '24px' }}>
          <Outlet />
        </div>
      </Content>
    </Layout>
  )
}
