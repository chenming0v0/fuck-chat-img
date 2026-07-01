import React from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@douyinfe/semi-ui'
import { Sun, Moon, LogOut, Boxes } from 'lucide-react'
import { useTheme } from '@/context/Theme'
import { useAuth } from '@/helpers/auth'

// 顶部栏：左 Logo + 名称，右 主题切换 / 用户名 / 登出
export default function HeaderBar() {
  const { isDark, toggleTheme } = useTheme()
  const { username, logout } = useAuth()
  const navigate = useNavigate()

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex items-center justify-between h-16 px-4 sm:px-6">
      {/* Logo + 名称 */}
      <div
        className="flex items-center gap-2 cursor-pointer select-none"
        onClick={() => navigate('/console')}
      >
        <div
          className="w-9 h-9 rounded-xl flex items-center justify-center text-white"
          style={{
            background:
              'linear-gradient(135deg, #6366f1 0%, #8b5cf6 50%, #14b8a6 100%)',
          }}
        >
          <Boxes size={20} />
        </div>
        <span className="text-lg font-semibold tracking-tight">
          Fuck Chat Img
        </span>
      </div>

      {/* 右侧操作 */}
      <div className="flex items-center gap-2">
        <Button
          theme="borderless"
          type="tertiary"
          icon={isDark ? <Sun size={18} /> : <Moon size={18} />}
          onClick={toggleTheme}
          aria-label="切换主题"
        />
        {username && (
          <span className="hidden sm:inline text-sm text-semi-color-text-1 px-2">
            {username}
          </span>
        )}
        <Button
          theme="borderless"
          type="tertiary"
          icon={<LogOut size={18} />}
          onClick={handleLogout}
        >
          登出
        </Button>
      </div>
    </div>
  )
}
