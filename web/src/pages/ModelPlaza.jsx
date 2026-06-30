import React, { useEffect, useState } from 'react'
import { Tag, Spin, Empty, Typography, Toast } from '@douyinfe/semi-ui'
import { Boxes } from 'lucide-react'
import CardPro from '@/components/common/ui/CardPro'
import { listGroups, pickMessage } from '@/helpers/api'

// 模型广场：卡片视图展示已启用模型组，提示对外暴露的模型名
export default function ModelPlaza() {
  const [groups, setGroups] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let active = true
    ;(async () => {
      try {
        const res = await listGroups({ size: 100 })
        if (active && res?.success) {
          // 仅展示已启用的
          setGroups((res.data || []).filter((g) => g.enabled))
        }
      } catch (e) {
        if (active) Toast.error(pickMessage(e, '加载模型列表失败'))
      } finally {
        if (active) setLoading(false)
      }
    })()
    return () => {
      active = false
    }
  }, [])

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">模型广场</h2>
        <p className="text-sm text-semi-color-text-2 mt-1">
          以下模型组当前已启用。本服务同时兼容三种协议：
          <code className="px-1.5 py-0.5 rounded bg-semi-color-fill-1">/v1/responses</code>、
          <code className="px-1.5 py-0.5 rounded bg-semi-color-fill-1">/v1/chat/completions</code>、
          <code className="px-1.5 py-0.5 rounded bg-semi-color-fill-1">/v1/messages</code>（Anthropic Claude 兼容），
          三者均支持真流式 SSE。调用时请将 <code className="px-1.5 py-0.5 rounded bg-semi-color-fill-1">model</code> 字段填写为模型组名称。
        </p>
      </div>

      <CardPro>
        {loading ? (
          <div className="flex justify-center py-12">
            <Spin size="large" />
          </div>
        ) : groups.length === 0 ? (
          <Empty
            title="暂无可用模型组"
            description="请前往「模型组管理」创建并启用模型组"
          />
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            {groups.map((g) => (
              <div
                key={g.id}
                className="!rounded-2xl p-5 transition hover:shadow-lg"
                style={{
                  border: '1px solid var(--semi-color-border)',
                  background: 'var(--semi-color-bg-0)',
                }}
              >
                <div className="flex items-start gap-3 mb-3">
                  <div
                    className="w-12 h-12 rounded-2xl flex items-center justify-center flex-shrink-0"
                    style={{
                      background: 'var(--semi-color-primary-light-active)',
                      border: '1px solid var(--semi-color-primary)',
                      color: 'var(--semi-color-primary)',
                    }}
                  >
                    <Boxes size={22} />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="font-semibold truncate">{g.name}</div>
                    <Typography.Text
                      type="tertiary"
                      size="small"
                      className="line-clamp-2"
                      style={{
                        display: '-webkit-box',
                        WebkitLineClamp: 2,
                        WebkitBoxOrient: 'vertical',
                        overflow: 'hidden',
                      }}
                    >
                      {g.description || '暂无描述'}
                    </Typography.Text>
                  </div>
                </div>

                <div className="flex flex-wrap gap-2 mb-3">
                  <Tag size="small" shape="circle" color="blue">
                    主模型: {g.main_text_model?.model || '-'}
                  </Tag>
                  <Tag size="small" shape="circle" color="cyan">
                    图片模型 ×{(g.image_models || []).length}
                  </Tag>
                  <Tag size="small" shape="circle" color="violet">
                    {g.image_strategy === 'failover' ? '故障转移' : '轮询'}
                  </Tag>
                </div>

                <div
                  className="text-xs px-3 py-2 rounded-lg"
                  style={{ background: 'var(--semi-color-fill-0)' }}
                >
                  对外模型名: <span className="font-mono font-semibold">{g.name}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardPro>
    </div>
  )
}
