import React from 'react'
import { Nav } from '@douyinfe/semi-ui'
import { useLocation, useNavigate } from 'react-router-dom'
import {
  LayoutDashboard,
  Boxes,
  Store,
  History as HistoryIcon,
  Settings,
  ChevronsLeft,
  ChevronsRight,
} from 'lucide-react'

// 侧边导航：分组「控制台」「个人」
export default function SiderBar({ collapsed, onToggle }) {
  const location = useLocation()
  const navigate = useNavigate()

  const items = [
    {
      itemKey: 'console',
      text: '控制台',
      items: [
        {
          itemKey: '/console',
          text: '仪表盘',
          icon: <LayoutDashboard size={16} />,
        },
        {
          itemKey: '/console/groups',
          text: '模型组管理',
          icon: <Boxes size={16} />,
        },
        {
          itemKey: '/console/plaza',
          text: '模型广场',
          icon: <Store size={16} />,
        },
        {
          itemKey: '/console/history',
          text: '历史记录',
          icon: <HistoryIcon size={16} />,
        },
      ],
    },
    {
      itemKey: 'personal',
      text: '个人',
      items: [
        {
          itemKey: '/console/settings',
          text: '设置',
          icon: <Settings size={16} />,
        },
      ],
    },
  ]

  function handleSelect(data) {
    if (data?.itemKey && data.itemKey.startsWith('/')) {
      navigate(data.itemKey)
    }
  }

  return (
    <div className="h-full flex flex-col">
      <div className="flex-1 overflow-y-auto scrollbar-hide">
        <Nav
          style={{ maxWidth: '100%', height: '100%' }}
          bodyStyle={{ height: '100%' }}
          items={items}
          selectedKeys={[location.pathname]}
          openKeys={['console', 'personal']}
          onSelect={handleSelect}
          header={{
            text: collapsed ? '' : '导航',
          }}
        />
      </div>

      {/* 底部折叠按钮 */}
      <div
        className="flex items-center justify-center cursor-pointer select-none"
        style={{
          height: 48,
          borderTop: '1px solid var(--semi-color-border)',
          color: 'var(--semi-color-text-2)',
        }}
        onClick={onToggle}
        title={collapsed ? '展开侧边栏' : '收起侧边栏'}
      >
        {collapsed ? <ChevronsRight size={18} /> : <ChevronsLeft size={18} />}
      </div>
    </div>
  )
}
