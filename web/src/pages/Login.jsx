import React, { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Card, Form, Button as SemiButton, Divider, Toast } from '@douyinfe/semi-ui'
import { IconMail, IconLock } from '@douyinfe/semi-icons'
import { Boxes, ArrowRight } from 'lucide-react'
import { login, getStatus, pickMessage } from '@/helpers/api'
import { useAuth } from '@/helpers/auth'

// 登录页：复刻 newapi 登录布局
// 若 /api/status 返回 need_setup=true(无任何用户), 自动跳转到 /setup
export default function Login() {
  const navigate = useNavigate()
  const location = useLocation()
  const { isAuthenticated, login: doLogin } = useAuth()
  const [loading, setLoading] = useState(false)

  // 从 Setup 页跳转过来时, 可携带刚设置的用户名预填
  const presetUsername = location.state?.username || ''

  useEffect(() => {
    let active = true
    if (isAuthenticated) {
      navigate('/console', { replace: true })
      active = false
    } else {
      // 检查是否需要首次设置管理员
      getStatus()
        .then((res) => {
          if (active && res?.success && res?.data?.need_setup) {
            navigate('/setup', { replace: true })
          }
        })
        .catch(() => {
          // 忽略: 即使状态检查失败也允许走登录流程
        })
    }
    return () => {
      active = false
    }
  }, [isAuthenticated, navigate])

  async function handleSubmit(values) {
    if (loading) return // 防止 Enter 键重复提交
    setLoading(true)
    try {
      const res = await login(values)
      if (res?.success) {
        doLogin(res.data)
        navigate('/console', { replace: true })
      } else {
        Toast.error(res?.message || '登录失败')
      }
    } catch (e) {
      Toast.error(pickMessage(e, '登录失败'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      className="relative flex items-center justify-center overflow-hidden"
      style={{ minHeight: '100vh', backgroundColor: 'var(--semi-color-bg-1)' }}
    >
      {/* 装饰球 */}
      <div className="blur-ball blur-ball-indigo" style={{ top: -120, right: -120 }} />
      <div className="blur-ball blur-ball-teal" style={{ bottom: -120, left: -120 }} />

      <div className="relative z-10 w-full px-4">
        <Card
          className="!rounded-2xl !border-0 mx-auto"
          style={{ maxWidth: 380, boxShadow: '0 12px 40px rgba(0,0,0,0.08)' }}
          bodyStyle={{ padding: 32 }}
        >
          {/* Logo + 标题 */}
          <div className="flex flex-col items-center mb-6">
            <div
              className="w-14 h-14 rounded-2xl flex items-center justify-center text-white mb-3"
              style={{
                background:
                  'linear-gradient(135deg, #6366f1 0%, #8b5cf6 50%, #14b8a6 100%)',
              }}
            >
              <Boxes size={28} />
            </div>
            <h1 className="text-xl font-semibold tracking-tight">
              Fuck Chat Img
            </h1>
            <p className="text-sm text-semi-color-text-2 mt-1">
              登录到管理控制台
            </p>
          </div>

          <Form onSubmit={handleSubmit} labelPosition="inset">
            <Form.Input
              field="username"
              label="用户名"
              placeholder="请输入用户名"
              prefix={<IconMail />}
              initValue={presetUsername}
              rules={[{ required: true, message: '请输入用户名' }]}
              showClear
            />
            <Form.Input
              field="password"
              label="密码"
              placeholder="请输入密码"
              mode="password"
              prefix={<IconLock />}
              rules={[{ required: true, message: '请输入密码' }]}
            />

            <SemiButton
              htmlType="submit"
              theme="solid"
              loading={loading}
              className="!rounded-full w-full !h-12"
              style={{
                backgroundColor: '#000',
                color: '#fff',
                marginTop: 8,
              }}
              icon={<ArrowRight size={18} />}
              iconPosition="right"
            >
              继续
            </SemiButton>
          </Form>

          <Divider margin={12} />
          <p className="text-center text-xs text-semi-color-text-2">
            登录即表示同意使用本服务
          </p>
        </Card>
      </div>
    </div>
  )
}
