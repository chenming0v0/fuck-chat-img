import React, { useEffect, useMemo, useRef, useState } from 'react'
import {
  Table,
  Tag,
  Button,
  SideSheet,
  Empty,
  Spin,
  Select,
  Typography,
  Pagination,
  Modal,
  Toast,
} from '@douyinfe/semi-ui'
import { Eye, Trash2, Image as ImageIcon } from 'lucide-react'
import dayjs from 'dayjs'
import CardPro from '@/components/common/ui/CardPro'
import {
  listHistory,
  getHistory,
  deleteHistory,
  clearHistory,
  historyStats,
  pickMessage,
} from '@/helpers/api'
import { useAuth } from '@/helpers/auth'

// 历史记录页：统计条 + 筛选 + 表格 + 侧滑详情
export default function History() {
  const { isAdmin } = useAuth()
  const [data, setData] = useState([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(10)
  // 搜索防抖: keywordInput 即时, keyword 防抖后用于请求, 防每键一请求与过期响应覆盖
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')
  const [group, setGroup] = useState(undefined)
  const [successFilter, setSuccessFilter] = useState(undefined)
  const [cacheHitFilter, setCacheHitFilter] = useState(undefined)
  const [stats, setStats] = useState(null)

  const [detailOpen, setDetailOpen] = useState(false)
  const [detail, setDetail] = useState(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const refreshReqIdRef = useRef(0)
  const statsReqIdRef = useRef(0)
  const detailReqIdRef = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  // 刷新列表与统计
  async function refresh() {
    const myId = ++refreshReqIdRef.current
    setLoading(true)
    try {
      const params = {
        p: page,
        size,
        keyword: keyword || undefined,
        group: group || undefined,
        success:
          successFilter === undefined ? undefined : successFilter ? 'true' : 'false',
        cache_hit:
          cacheHitFilter === undefined
            ? undefined
            : cacheHitFilter
              ? 'true'
              : 'false',
      }
      const res = await listHistory(params)
      if (myId !== refreshReqIdRef.current || !mountedRef.current) return
      if (res?.success) {
        setData(res.data || [])
        setTotal(res.total || 0)
      }
    } catch (e) {
      if (myId !== refreshReqIdRef.current || !mountedRef.current) return
      Toast.error(pickMessage(e, '加载历史记录失败'))
    } finally {
      if (myId === refreshReqIdRef.current && mountedRef.current) {
        setLoading(false)
      }
    }
  }

  async function refreshStats() {
    const myId = ++statsReqIdRef.current
    try {
      const res = await historyStats()
      if (myId !== statsReqIdRef.current || !mountedRef.current) return
      if (res?.success) setStats(res.data)
    } catch (e) {
      // 统计失败不阻断主列表
    }
  }

  // keyword 防抖
  useEffect(() => {
    const t = setTimeout(() => {
      setKeyword(keywordInput)
      setPage(1)
    }, 300)
    return () => clearTimeout(t)
  }, [keywordInput])

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, size, group, successFilter, cacheHitFilter, keyword])

  useEffect(() => {
    refreshStats()
  }, [])

  async function openDetail(row) {
    const myId = ++detailReqIdRef.current
    setDetailOpen(true)
    setDetailLoading(true)
    setDetail(null)
    try {
      const res = await getHistory(row.id)
      if (myId !== detailReqIdRef.current || !mountedRef.current) return
      if (res?.success) {
        setDetail(res.data)
      } else {
        // 兜底用列表数据
        setDetail(row)
      }
    } catch (e) {
      if (myId !== detailReqIdRef.current || !mountedRef.current) return
      setDetail(row)
    } finally {
      if (myId === detailReqIdRef.current && mountedRef.current) {
        setDetailLoading(false)
      }
    }
  }

  function handleDelete(row) {
    Modal.confirm({
      title: '删除记录',
      content: `确定要删除请求 ${row.request_id} 的记录吗？`,
      onOk: async () => {
        try {
          const res = await deleteHistory(row.id)
          if (res?.success) {
            Toast.success('已删除')
            refresh()
            refreshStats()
          } else {
            Toast.error(res?.message || '删除失败')
          }
        } catch (e) {
          Toast.error(pickMessage(e, '删除失败'))
        }
      },
    })
  }

  function handleClearAll() {
    Modal.confirm({
      title: '清空所有历史',
      content: '该操作不可恢复，确定要清空全部历史记录吗？',
      onOk: async () => {
        try {
          const res = await clearHistory()
          if (res?.success) {
            Toast.success('已清空')
            setPage(1)
            refresh()
            refreshStats()
          } else {
            Toast.error(res?.message || '清空失败')
          }
        } catch (e) {
          Toast.error(pickMessage(e, '清空失败'))
        }
      },
    })
  }

  const cacheRate = useMemo(() => {
    if (!stats || !stats.total) return '0%'
    return ((stats.cache_hit / stats.total) * 100).toFixed(1) + '%'
  }, [stats])

  const columns = [
    {
      title: '时间',
      dataIndex: 'created_at',
      width: 160,
      render: (v) => (v ? dayjs(v).format('YYYY-MM-DD HH:mm:ss') : '-'),
    },
    {
      title: '请求 ID',
      dataIndex: 'request_id',
      width: 180,
      render: (v) => (
        <Typography.Text ellipsis={{ showTooltip: true }} style={{ maxWidth: 160 }}>
          {v || '-'}
        </Typography.Text>
      ),
    },
    {
      title: '模型组',
      dataIndex: 'model_group',
      width: 140,
      render: (v) => <Tag shape="circle" color="blue">{v || '-'}</Tag>,
    },
    {
      title: '端点',
      dataIndex: 'endpoint',
      width: 140,
      render: (v) => <span className="text-xs font-mono">{v || '-'}</span>,
    },
    {
      title: '图片',
      dataIndex: 'has_image',
      width: 100,
      render: (v, row) =>
        v ? (
          <Tag shape="circle" color="cyan" prefixIcon={<ImageIcon size={12} />}>
            {row.image_count || 1}
          </Tag>
        ) : (
          <span className="text-semi-color-text-3">-</span>
        ),
    },
    {
      title: '缓存',
      dataIndex: 'cache_hit',
      width: 90,
      render: (v) =>
        v ? (
          <Tag shape="circle" color="green">命中</Tag>
        ) : (
          <Tag shape="circle" color="grey">未命中</Tag>
        ),
    },
    {
      title: '状态',
      dataIndex: 'success',
      width: 90,
      render: (v) =>
        v ? (
          <Tag shape="circle" color="green">成功</Tag>
        ) : (
          <Tag shape="circle" color="red">失败</Tag>
        ),
    },
    {
      title: 'Token',
      dataIndex: 'total_tokens',
      width: 90,
      render: (v) => v ?? '-',
    },
    {
      title: '延迟',
      dataIndex: 'latency_ms',
      width: 90,
      render: (v) => (v != null ? `${Math.round(v)} ms` : '-'),
    },
    {
      title: '操作',
      width: 140,
      fixed: 'right',
      render: (_, row) => (
        <div className="flex items-center gap-1">
          <Button
            size="small"
            theme="borderless"
            icon={<Eye size={14} />}
            onClick={() => openDetail(row)}
          >
            详情
          </Button>
          {isAdmin && (
            <Button
              size="small"
              theme="borderless"
              type="danger"
              icon={<Trash2 size={14} />}
              onClick={() => handleDelete(row)}
            />
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">历史记录</h2>
          <p className="text-sm text-semi-color-text-2 mt-1">
            查看所有请求的执行结果与详细信息。
          </p>
        </div>
        {isAdmin && (
          <Button
            theme="solid"
            type="danger"
            className="!rounded-full"
            onClick={handleClearAll}
          >
            清空全部
          </Button>
        )}
      </div>

      {/* 统计条 */}
      <CardPro>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <StatBlock label="总请求" value={stats?.total ?? 0} color="var(--semi-color-primary)" />
          <StatBlock label="缓存命中" value={stats?.cache_hit ?? 0} color="var(--semi-color-success)" />
          <StatBlock label="缓存命中率" value={cacheRate} color="var(--semi-color-warning)" />
          <StatBlock label="今日请求" value={stats?.today ?? 0} color="var(--semi-color-info)" />
        </div>
      </CardPro>

      {/* 表格 */}
      <CardPro
        title="请求列表"
        extra={
          <input
            className="!rounded-full px-4 py-1.5 text-sm"
            style={{
              border: '1px solid var(--semi-color-border)',
              background: 'var(--semi-color-bg-0)',
              color: 'var(--semi-color-text-0)',
              outline: 'none',
              minWidth: 200,
            }}
            placeholder="搜索请求 ID / 模型组"
            value={keywordInput}
            onChange={(e) => {
              setKeywordInput(e.target.value)
            }}
          />
        }
        footer={
          <div className="flex items-center justify-between">
            <span className="text-sm text-semi-color-text-2">
              共 {total} 条
            </span>
            <Pagination
              total={total}
              currentPage={page}
              pageSize={size}
              onPageChange={(p) => setPage(p)}
              onPageSizeChange={(s) => {
                setSize(s)
                setPage(1)
              }}
              showSizeChanger
              pageSizeOpts={[10, 20, 50]}
            />
          </div>
        }
      >
        {/* 筛选行 */}
        <div className="flex flex-wrap gap-3 mb-4">
          <Select
            placeholder="成功状态"
            style={{ width: 140 }}
            value={successFilter}
            onChange={(v) => {
              setSuccessFilter(v)
              setPage(1)
            }}
            optionList={[
              { label: '全部', value: undefined },
              { label: '成功', value: true },
              { label: '失败', value: false },
            ]}
          />
          <Select
            placeholder="缓存命中"
            style={{ width: 140 }}
            value={cacheHitFilter}
            onChange={(v) => {
              setCacheHitFilter(v)
              setPage(1)
            }}
            optionList={[
              { label: '全部', value: undefined },
              { label: '命中', value: true },
              { label: '未命中', value: false },
            ]}
          />
        </div>

        <Table
          columns={columns}
          dataSource={data}
          rowKey="id"
          loading={loading}
          pagination={false}
          scroll={{ x: 1300 }}
          empty={<Empty title="暂无历史记录" />}
        />
      </CardPro>

      {/* 详情侧滑 */}
      <SideSheet
        title="请求详情"
        visible={detailOpen}
        onCancel={() => setDetailOpen(false)}
        width={500}
      >
        {detailLoading ? (
          <div className="flex justify-center py-12">
            <Spin size="large" />
          </div>
        ) : detail ? (
          <div className="space-y-4">
            <DetailRow label="请求 ID" value={detail.request_id} mono />
            <DetailRow label="模型组" value={detail.model_group} />
            <DetailRow label="端点" value={detail.endpoint} mono />
            <DetailRow
              label="主模型"
              value={detail.main_model_used || '-'}
            />
            <DetailRow
              label="图片模型"
              value={detail.image_model_used || '-'}
            />
            <DetailRow
              label="创建时间"
              value={
                detail.created_at
                  ? dayjs(detail.created_at).format('YYYY-MM-DD HH:mm:ss')
                  : '-'
              }
            />
            <DetailRow
              label="状态"
              value={detail.success ? '成功' : '失败'}
            />
            <DetailRow
              label="Token"
              value={`${detail.prompt_tokens || 0} + ${detail.completion_tokens || 0} = ${detail.total_tokens || 0}`}
            />
            <DetailRow
              label="延迟"
              value={detail.latency_ms != null ? `${Math.round(detail.latency_ms)} ms` : '-'}
            />
            <DetailRow label="缓存" value={detail.cache_hit ? '命中' : '未命中'} />
            <DetailRow label="图片" value={detail.has_image ? `是 (${detail.image_count || 1})` : '否'} />

            {detail.error_message && (
              <div>
                <div className="text-xs text-semi-color-text-2 mb-1">错误信息</div>
                <div
                  className="text-sm p-3 rounded-lg font-mono"
                  style={{
                    background: 'var(--semi-color-danger-light-default)',
                    color: 'var(--semi-color-danger)',
                  }}
                >
                  {detail.error_message}
                </div>
              </div>
            )}

            {detail.input_summary && (
              <div>
                <div className="text-xs text-semi-color-text-2 mb-1">输入摘要</div>
                <pre
                  className="text-sm p-3 rounded-lg overflow-auto"
                  style={{
                    background: 'var(--semi-color-fill-0)',
                    color: 'var(--semi-color-text-1)',
                    maxHeight: 240,
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-word',
                  }}
                >
                  {detail.input_summary}
                </pre>
              </div>
            )}

            {detail.output_summary && (
              <div>
                <div className="text-xs text-semi-color-text-2 mb-1">输出摘要</div>
                <pre
                  className="text-sm p-3 rounded-lg overflow-auto"
                  style={{
                    background: 'var(--semi-color-fill-0)',
                    color: 'var(--semi-color-text-1)',
                    maxHeight: 240,
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-word',
                  }}
                >
                  {detail.output_summary}
                </pre>
              </div>
            )}
          </div>
        ) : (
          <Empty title="无数据" />
        )}
      </SideSheet>
    </div>
  )
}

function StatBlock({ label, value, color }) {
  return (
    <div
      className="!rounded-2xl p-4"
      style={{
        border: '1px solid var(--semi-color-border)',
        background: 'var(--semi-color-bg-0)',
      }}
    >
      <div className="text-sm text-semi-color-text-2 mb-2">{label}</div>
      <div className="text-2xl font-semibold" style={{ color }}>
        {value}
      </div>
    </div>
  )
}

function DetailRow({ label, value, mono }) {
  return (
    <div className="flex items-start gap-3">
      <div className="text-xs text-semi-color-text-2 w-20 flex-shrink-0 pt-1">
        {label}
      </div>
      <div
        className={`flex-1 text-sm ${mono ? 'font-mono' : ''}`}
        style={{ color: 'var(--semi-color-text-0)', wordBreak: 'break-word' }}
      >
        {value || '-'}
      </div>
    </div>
  )
}
