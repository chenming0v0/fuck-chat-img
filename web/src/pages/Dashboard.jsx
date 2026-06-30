import React, { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Tag, Spin, Empty } from '@douyinfe/semi-ui'
import {
  Activity,
  CheckCircle2,
  XCircle,
  Zap,
  CalendarDays,
  Clock,
  Hash,
  Database,
  Store,
} from 'lucide-react'
import CardPro from '@/components/common/ui/CardPro'
import { historyStats, pickMessage } from '@/helpers/api'
import { useAuth } from '@/helpers/auth'
import { toast } from 'react-toastify'

// 仪表盘：展示历史统计与模型组广场入口
export default function Dashboard() {
  const { username } = useAuth()
  const navigate = useNavigate()
  const [stats, setStats] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let active = true
    ;(async () => {
      try {
        const res = await historyStats()
        if (active && res?.success) {
          setStats(res.data)
        }
      } catch (e) {
        if (active) toast.error(pickMessage(e, '加载统计数据失败'))
      } finally {
        if (active) setLoading(false)
      }
    })()
    return () => {
      active = false
    }
  }, [])

  const cards = [
    {
      label: '总请求',
      value: stats?.total ?? 0,
      icon: <Activity size={18} />,
      color: 'var(--semi-color-primary)',
    },
    {
      label: '成功',
      value: stats?.success ?? 0,
      icon: <CheckCircle2 size={18} />,
      color: 'var(--semi-color-success)',
    },
    {
      label: '失败',
      value: stats?.fail ?? 0,
      icon: <XCircle size={18} />,
      color: 'var(--semi-color-danger)',
    },
    {
      label: '缓存命中',
      value: stats?.cache_hit ?? 0,
      icon: <Zap size={18} />,
      color: 'var(--semi-color-warning)',
    },
    {
      label: '今日请求',
      value: stats?.today ?? 0,
      icon: <CalendarDays size={18} />,
      color: 'var(--semi-color-info)',
    },
    {
      label: '平均延迟',
      value: stats?.avg_latency ? `${Math.round(stats.avg_latency)} ms` : '0 ms',
      icon: <Clock size={18} />,
      color: 'var(--semi-color-tertiary)',
    },
    {
      label: '总 Token',
      value: stats?.total_tokens ?? 0,
      icon: <Hash size={18} />,
      color: 'var(--semi-color-secondary)',
    },
    {
      label: '缓存条目',
      value: stats?.cache_stats?.items ?? 0,
      icon: <Database size={18} />,
      color: 'var(--semi-color-success)',
    },
  ]

  return (
    <div className="space-y-6">
      {/* 欢迎语 */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">
            欢迎回来{username ? `，${username}` : ''} 👋
          </h2>
          <p className="text-sm text-semi-color-text-2 mt-1">
            在这里管理你的图片对话模型组与查看运行统计。
          </p>
        </div>
      </div>

      {/* 统计卡片网格 */}
      <CardPro title="运行统计" extra={<Tag shape="circle" color="white">实时</Tag>}>
        {loading ? (
          <div className="flex justify-center py-12">
            <Spin size="large" />
          </div>
        ) : (
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            {cards.map((c) => (
              <div
                key={c.label}
                className="!rounded-2xl p-4"
                style={{
                  border: '1px solid var(--semi-color-border)',
                  background: 'var(--semi-color-bg-0)',
                }}
              >
                <div className="flex items-center justify-between mb-3">
                  <span className="text-sm text-semi-color-text-2">
                    {c.label}
                  </span>
                  <span style={{ color: c.color }}>{c.icon}</span>
                </div>
                <div className="text-2xl font-semibold tracking-tight">
                  {c.value}
                </div>
              </div>
            ))}
          </div>
        )}
      </CardPro>

      {/* 缓存详情 */}
      {stats?.cache_stats && (
        <CardPro title="缓存详情">
          <div className="flex flex-wrap gap-4">
            <Tag size="large" shape="circle" color="blue">
              是否启用: {stats.cache_stats.enabled ? '是' : '否'}
            </Tag>
            <Tag size="large" shape="circle" color="cyan">
              条目: {stats.cache_stats.items} / {stats.cache_stats.max_items}
            </Tag>
            <Tag size="large" shape="circle" color="green">
              命中: {stats.cache_stats.hits}
            </Tag>
            <Tag size="large" shape="circle" color="orange">
              未命中: {stats.cache_stats.misses}
            </Tag>
          </div>
        </CardPro>
      )}

      {/* 模型广场入口 */}
      <CardPro title="快速入口">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div
            className="!rounded-2xl p-5 cursor-pointer transition hover:shadow-lg"
            style={{
              border: '1px solid var(--semi-color-border)',
              background: 'var(--semi-color-bg-0)',
            }}
            onClick={() => navigate('/console/plaza')}
          >
            <div className="flex items-center gap-3 mb-2">
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center text-white"
                style={{
                  background:
                    'linear-gradient(135deg, #6366f1 0%, #14b8a6 100%)',
                }}
              >
                <Store size={20} />
              </div>
              <div>
                <div className="font-semibold">模型组广场</div>
                <div className="text-xs text-semi-color-text-2">
                  查看所有已启用的模型组及其对外模型名
                </div>
              </div>
            </div>
          </div>
          <div
            className="!rounded-2xl p-5 cursor-pointer transition hover:shadow-lg"
            style={{
              border: '1px solid var(--semi-color-border)',
              background: 'var(--semi-color-bg-0)',
            }}
            onClick={() => navigate('/console/history')}
          >
            <div className="flex items-center gap-3 mb-2">
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center text-white"
                style={{
                  background:
                    'linear-gradient(135deg, #14b8a6 0%, #6366f1 100%)',
                }}
              >
                <Activity size={20} />
              </div>
              <div>
                <div className="font-semibold">历史记录</div>
                <div className="text-xs text-semi-color-text-2">
                  查看请求日志、Token 消耗与错误信息
                </div>
              </div>
            </div>
          </div>
        </div>
      </CardPro>
    </div>
  )
}
