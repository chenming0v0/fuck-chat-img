import React, { useState } from 'react'
import { Form, Button, RadioGroup, Radio, Toast } from '@douyinfe/semi-ui'
import { toast } from 'react-toastify'
import CardPro from '@/components/common/ui/CardPro'
import { changePassword } from '@/helpers/api'
import { useTheme } from '@/context/Theme'

// 设置页：修改密码 + 主题切换
export default function Settings() {
  const { theme, setTheme } = useTheme()
  const [loading, setLoading] = useState(false)

  async function handleSubmit(values) {
    const { old_password, new_password, confirm } = values
    if (new_password !== confirm) {
      toast.error('两次输入的新密码不一致')
      return
    }
    if (!new_password || new_password.length < 4) {
      toast.error('新密码至少 4 位')
      return
    }
    setLoading(true)
    try {
      const res = await changePassword({ old_password, new_password })
      if (res?.success) {
        toast.success('密码修改成功')
      } else {
        toast.error(res?.message || '修改失败')
      }
    } catch (e) {
      toast.error(e?.response?.data?.message || e?.message || '修改失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">设置</h2>
        <p className="text-sm text-semi-color-text-2 mt-1">
          修改密码与界面主题。
        </p>
      </div>

      <CardPro title="修改密码">
        <Form
          onSubmit={handleSubmit}
          labelPosition="left"
          labelWidth={100}
          style={{ maxWidth: 480 }}
        >
          <Form.Input
            field="old_password"
            label="原密码"
            mode="password"
            rules={[{ required: true, message: '请输入原密码' }]}
          />
          <Form.Input
            field="new_password"
            label="新密码"
            mode="password"
            rules={[{ required: true, message: '请输入新密码' }]}
          />
          <Form.Input
            field="confirm"
            label="确认新密码"
            mode="password"
            rules={[{ required: true, message: '请再次输入新密码' }]}
          />
          <div className="mt-4">
            <Button
              htmlType="submit"
              theme="solid"
              loading={loading}
              className="!rounded-full"
            >
              保存修改
            </Button>
          </div>
        </Form>
      </CardPro>

      <CardPro title="主题">
        <Form labelPosition="left" labelWidth={100} style={{ maxWidth: 480 }}>
          <Form.Slot label="界面主题">
            <RadioGroup
              value={theme}
              onChange={(e) => setTheme(e.target.value)}
              type="button"
            >
              <Radio value="light">亮色</Radio>
              <Radio value="dark">暗色</Radio>
            </RadioGroup>
          </Form.Slot>
        </Form>
      </CardPro>
    </div>
  )
}
