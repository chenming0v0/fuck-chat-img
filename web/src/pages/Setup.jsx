import React, { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Card, Form, Button as SemiButton, Divider, Typography, Spin, Toast } from '@douyinfe/semi-ui'
import { IconUser, IconLock } from '@douyinfe/semi-icons'
import { Boxes, ArrowRight, ShieldCheck } from 'lucide-react'
import { setup as setupApi, getStatus, pickMessage } from '@/helpers/api'

const { Text } = Typography

// 首次启动设置页: 当数据库没有任何用户时, 引导用户设置管理员账号密码
// 设置成功后自动跳转登录页
// 反向校验: 若已存在管理员(无需 setup), 直接跳转登录页, 避免用户卡在死页
export default function Setup() {
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(true)

  useEffect(() => {
    let active = true
    getStatus()
      .then((res) => {
        if (!active) return
        // need_setup=false 表示已有管理员, 不应停留在设置页
        if (res && res.data && res.data.need_setup === false) {
          navigate('/login', { replace: true })
        }
      })
      .catch(() => {
        // 状态接口失败时不阻断, 允许尝试提交(后端会返回 409)
      })
      .finally(() => {
        if (active) setChecking(false)
      })
    return () => {
      active = false
    }
  }, [navigate])

  async function handleSubmit(values) {
    if (loading) return // 防止 Enter 键重复提交
    if (values.password !== values.confirmPassword) {
      Toast.error('两次输入的密码不一致')
      return
    }
    setLoading(true)
    try {
      const res = await setupApi({
        username: values.username,
        password: values.password,
      })
      if (res?.success) {
        Toast.success(res?.message || '设置成功, 请登录')
        // 把刚设置的用户名带到登录页, 免去再次手敲
        navigate('/login', { replace: true, state: { username: values.username } })
      } else {
        Toast.error(res?.message || '设置失败')
      }
    } catch (e) {
      // 409 = 已有管理员(可能被并发抢占或已设置过), 跳转登录页而非卡死
      if (e?.response?.status === 409) {
        Toast.error('管理员账户已存在, 请直接登录')
        navigate('/login', { replace: true })
        return
      }
      Toast.error(pickMessage(e, '设置失败'))
    } finally {
      setLoading(false)
    }
  }

  if (checking) {
    return (
      <div className="flex items-center justify-center" style={{ minHeight: '100vh' }}>
        <Spin size="large" />
      </div>
    )
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
          style={{ maxWidth: 420, boxShadow: '0 12px 40px rgba(0,0,0,0.08)' }}
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
              初始化管理员账户
            </h1>
            <p className="text-sm text-semi-color-text-2 mt-1 text-center">
              首次启动, 请设置你的管理员账号与密码
            </p>
          </div>

          {/* 提示条 */}
          <div
            className="flex items-start gap-2 mb-4 px-3 py-2 rounded-lg"
            style={{
              backgroundColor: 'var(--semi-color-primary-light-active)',
              border: '1px solid var(--semi-color-primary-light-default)',
            }}
          >
            <ShieldCheck size={16} className="flex-shrink-0 mt-0.5" style={{ color: 'var(--semi-color-primary)' }} />
            <Text type="tertiary" size="small">
              此设置仅在首次启动时可用. 设置完成后请妥善保管账号密码.
            </Text>
          </div>

          <Form onSubmit={handleSubmit} labelPosition="inset">
            <Form.Input
              field="username"
              label="管理员用户名"
              placeholder="请设置管理员用户名"
              prefix={<IconUser />}
              rules={[
                { required: true, message: '请输入用户名' },
                { min: 2, message: '用户名至少 2 个字符' },
              ]}
              showClear
            />
            <Form.Input
              field="password"
              label="密码"
              placeholder="至少 8 位"
              mode="password"
              prefix={<IconLock />}
              rules={[
                { required: true, message: '请输入密码' },
                { min: 8, message: '密码至少 8 位' },
              ]}
            />
            <Form.Input
              field="confirmPassword"
              label="确认密码"
              placeholder="再次输入密码"
              mode="password"
              prefix={<IconLock />}
              rules={[{ required: true, message: '请再次输入密码' }]}
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
              完成设置
            </SemiButton>
          </Form>

          <Divider margin={12} />
          <p className="text-center text-xs text-semi-color-text-2">
            设置完成后将跳转到登录页
          </p>
        </Card>
      </div>
    </div>
  )
}
