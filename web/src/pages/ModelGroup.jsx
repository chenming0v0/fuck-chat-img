import React, { useEffect, useMemo, useRef, useState } from 'react'
import {
  Table,
  Tag,
  Button,
  Modal,
  Form,
  Switch,
  RadioGroup,
  Radio,
  Pagination,
  Empty,
  Spin,
  Divider,
  Typography,
  Toast,
} from '@douyinfe/semi-ui'
import { Plus, Search, Pencil, Trash2, Boxes } from 'lucide-react'
import dayjs from 'dayjs'
import CardPro from '@/components/common/ui/CardPro'
import {
  listGroups,
  getGroupPlain,
  createGroup,
  updateGroup,
  deleteGroup,
  toggleGroup,
  pickMessage,
} from '@/helpers/api'
import { useAuth } from '@/helpers/auth'

// 默认上游模型对象
let _imgUidCounter = 0
function defaultUpstream() {
  _imgUidCounter += 1
  return {
    _uid: _imgUidCounter,
    base_url: '',
    api_key: '',
    model: '',
    api_type: 'openai',
    extra_url: '',
    max_retries: 1,
    weight: 1,
  }
}

// 默认表单值
const DEFAULT_FORM = {
  name: '',
  description: '',
  main_text_model: defaultUpstream(),
  image_models: [defaultUpstream()],
  image_strategy: 'round_robin',
  image_prompt: '',
  replace_image: false,
  enabled: true,
}

export default function ModelGroup() {
  const { isAdmin } = useAuth()
  const [data, setData] = useState([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(10)
  // 搜索防抖: keywordInput 即时跟随输入框, keyword 防抖后用于实际请求,
  // 避免每敲一个字就发请求, 同时防过期响应覆盖(防抖期间只发最后一次).
  const [keywordInput, setKeywordInput] = useState('')
  const [keyword, setKeyword] = useState('')

  const [modalVisible, setModalVisible] = useState(false)
  const [modalLoading, setModalLoading] = useState(false)
  const [editingId, setEditingId] = useState(null)
  const [formValues, setFormValues] = useState(DEFAULT_FORM)
  // 手动管理图片模型数组（与 Form 同步）
  const [imageModels, setImageModels] = useState([defaultUpstream()])
  const [imageModelsErrors, setImageModelsErrors] = useState({})
  const [togglingId, setTogglingId] = useState(null)
  const refreshReqIdRef = useRef(0)
  const editReqIdRef = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  async function refresh() {
    const myId = ++refreshReqIdRef.current
    setLoading(true)
    try {
      const res = await listGroups({
        p: page,
        size,
        keyword: keyword || undefined,
      })
      if (myId !== refreshReqIdRef.current || !mountedRef.current) return
      if (res?.success) {
        setData(res.data || [])
        setTotal(res.total || 0)
      }
    } catch (e) {
      if (myId !== refreshReqIdRef.current || !mountedRef.current) return
      Toast.error(pickMessage(e, '加载模型组列表失败'))
    } finally {
      if (myId === refreshReqIdRef.current && mountedRef.current) {
        setLoading(false)
      }
    }
  }

  // keyword 防抖: 输入停顿 300ms 后才更新 keyword, 触发 refresh
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
  }, [page, size, keyword])

  // 打开新建 Modal
  function openCreate() {
    setEditingId(null)
    setFormValues(DEFAULT_FORM)
    setImageModels([defaultUpstream()])
    setImageModelsErrors({})
    setModalVisible(true)
  }

  // 打开编辑 Modal：先拉详情获取明文 api_key
  async function openEdit(row) {
    const myId = ++editReqIdRef.current
    setEditingId(row.id)
    setModalVisible(true)
    setModalLoading(true)
    try {
      const res = await getGroupPlain(row.id)
      if (myId !== editReqIdRef.current || !mountedRef.current) return
      if (res?.success && res.data) {
        const g = res.data
        const main = {
          base_url: g.main_text_model?.base_url || '',
          api_key: g.main_text_model?.api_key || '',
          model: g.main_text_model?.model || '',
          api_type: g.main_text_model?.api_type || 'openai',
          extra_url: g.main_text_model?.extra_url || '',
          max_retries:
            g.main_text_model?.max_retries != null
              ? g.main_text_model?.max_retries
              : 1,
          weight:
            g.main_text_model?.weight != null ? g.main_text_model?.weight : 1,
        }
        const imgs = (g.image_models || []).map((m) => {
          _imgUidCounter += 1
          return {
            _uid: _imgUidCounter,
            base_url: m.base_url || '',
            api_key: m.api_key || '',
            model: m.model || '',
            api_type: m.api_type || 'openai',
            extra_url: m.extra_url || '',
            max_retries: m.max_retries != null ? m.max_retries : 1,
            weight: m.weight != null ? m.weight : 1,
          }
        })
        const vals = {
          name: g.name || '',
          description: g.description || '',
          main_text_model: main,
          image_models: imgs.length ? imgs : [defaultUpstream()],
          image_strategy: g.image_strategy || 'round_robin',
          image_prompt: g.image_prompt || '',
          replace_image: !!g.replace_image,
          enabled: !!g.enabled,
        }
        setFormValues(vals)
        setImageModels(vals.image_models)
        setImageModelsErrors({})
      } else {
        Toast.error(res?.message || '加载详情失败')
        setModalVisible(false)
      }
    } catch (e) {
      if (myId !== editReqIdRef.current || !mountedRef.current) return
      Toast.error(pickMessage(e, '加载详情失败'))
      setModalVisible(false)
    } finally {
      if (myId === editReqIdRef.current && mountedRef.current) {
        setModalLoading(false)
      }
    }
  }

  // 切换启用状态：直接调 toggle
  async function handleToggle(row) {
    if (togglingId) return
    setTogglingId(row.id)
    try {
      const res = await toggleGroup(row.id)
      if (!mountedRef.current) return
      if (res?.success) {
        Toast.success(res.data?.enabled ? '已启用' : '已停用')
        refresh()
      } else {
        Toast.error(res?.message || '切换失败')
      }
    } catch (e) {
      if (!mountedRef.current) return
      Toast.error(pickMessage(e, '切换失败'))
    } finally {
      if (mountedRef.current) {
        setTogglingId(null)
      }
    }
  }

  // 删除
  function handleDelete(row) {
    Modal.confirm({
      title: '删除模型组',
      content: `确定要删除模型组「${row.name}」吗？该操作不可恢复。`,
      onOk: async () => {
        try {
          const res = await deleteGroup(row.id)
          if (res?.success) {
            Toast.success('已删除')
            // 删除最后一条记录时回退页码, 避免停留在空表格页(与 History.jsx 一致)
            if (data.length === 1 && page > 1) {
              setPage(page - 1)
            } else {
              refresh()
            }
          } else {
            Toast.error(res?.message || '删除失败')
          }
        } catch (e) {
          Toast.error(pickMessage(e, '删除失败'))
        }
      },
    })
  }

  // 图片模型数组操作
  function addImageModel() {
    // defaultUpstream() 移到 updater 外部, 避免 React StrictMode 双重调用导致 _uid 跳号
    const next = defaultUpstream()
    setImageModels((prev) => [...prev, next])
  }
  function removeImageModel(idx) {
    setImageModels((prev) => {
      if (prev.length <= 1) {
        return prev
      }
      return prev.filter((_, i) => i !== idx)
    })
    // 副作用放在 setState updater 外, 避免 React StrictMode 双重调用导致重复弹 Toast
    if (imageModels.length <= 1) {
      Toast.warning('至少需要 1 个图片模型')
    }
  }
  function updateImageModel(idx, key, value) {
    setImageModels((prev) =>
      prev.map((m, i) => (i === idx ? { ...m, [key]: value } : m)),
    )
    setImageModelsErrors((prev) => {
      if (!prev[idx]) return prev
      const newErrs = { ...prev }
      const idxErrs = { ...newErrs[idx] }
      delete idxErrs[key]
      if (Object.keys(idxErrs).length === 0) {
        delete newErrs[idx]
      } else {
        newErrs[idx] = idxErrs
      }
      return newErrs
    })
  }

  // 提交表单
  async function handleSubmit(values) {
    const errors = {}
    imageModels.forEach((m, idx) => {
      const errs = {}
      if (!m.base_url) errs.base_url = true
      if (!m.api_key) errs.api_key = true
      if (!m.model) errs.model = true
      if (Object.keys(errs).length) errors[idx] = errs
    })
    setImageModelsErrors(errors)
    if (Object.keys(errors).length) {
      Toast.error('请填写图片模型的必填字段')
      return
    }

    const payload = {
      name: values.name,
      description: values.description || '',
      main_text_model: {
        base_url: values['main_text_model.base_url'] || '',
        api_key: values['main_text_model.api_key'] || '',
        model: values['main_text_model.model'] || '',
        api_type: values['main_text_model.api_type'] || 'openai',
        extra_url: values['main_text_model.extra_url'] || '',
        max_retries: Number(values['main_text_model.max_retries'] || 1),
        weight: Number(values['main_text_model.weight'] || 1),
      },
      image_models: imageModels.map((m) => ({
        base_url: m.base_url || '',
        api_key: m.api_key || '',
        model: m.model || '',
        api_type: m.api_type || 'openai',
        extra_url: m.extra_url || '',
        max_retries: Number(m.max_retries || 1),
        weight: Number(m.weight || 1),
      })),
      image_strategy: values.image_strategy || 'round_robin',
      image_prompt: values.image_prompt || '',
      replace_image: !!values.replace_image,
      enabled: !!values.enabled,
    }

    if (!payload.name) {
      Toast.error('请填写名称')
      return
    }
    if (!payload.main_text_model.base_url || !payload.main_text_model.api_key || !payload.main_text_model.model) {
      Toast.error('请填写主对话模型的必填字段')
      return
    }
    if (!payload.image_models.length) {
      Toast.error('至少需要 1 个图片模型')
      return
    }

    setModalLoading(true)
    try {
      let res
      if (editingId) {
        res = await updateGroup(editingId, payload)
      } else {
        res = await createGroup(payload)
      }
      if (!mountedRef.current) return
      if (res?.success) {
        Toast.success(editingId ? '已更新' : '已创建')
        setModalVisible(false)
        refresh()
      } else {
        Toast.error(res?.message || '保存失败')
      }
    } catch (e) {
      if (!mountedRef.current) return
      Toast.error(pickMessage(e, '保存失败'))
    } finally {
      if (mountedRef.current) {
        setModalLoading(false)
      }
    }
  }

  // columns 不做 useMemo: 内部闭包依赖 page/size/keyword/refresh 等响应式状态,
      // memo 会导致切换/删除后用过期闭包刷新到错误页码(与 History.jsx 保持一致)
  const columns = [
      {
        title: '名称',
        dataIndex: 'name',
        width: 160,
        render: (v) => <Tag shape="circle" color="blue" size="large">{v}</Tag>,
      },
      {
        title: '描述',
        dataIndex: 'description',
        render: (v) => (
          <Typography.Text
            ellipsis={{ showTooltip: true }}
            style={{ maxWidth: 220 }}
            type="tertiary"
          >
            {v || '-'}
          </Typography.Text>
        ),
      },
      {
        title: '主对话模型',
        dataIndex: 'main_text_model',
        width: 180,
        render: (v) => (
          <span className="text-sm font-mono">{v?.model || '-'}</span>
        ),
      },
      {
        title: '图片模型',
        dataIndex: 'image_models',
        width: 110,
        render: (v) => (
          <Tag shape="circle" color="cyan">×{Array.isArray(v) ? v.length : 0}</Tag>
        ),
      },
      {
        title: '策略',
        dataIndex: 'image_strategy',
        width: 110,
        render: (v) =>
          v === 'failover' ? (
            <Tag shape="circle" color="violet">故障转移</Tag>
          ) : (
            <Tag shape="circle" color="teal">轮询</Tag>
          ),
      },
      {
        title: '状态',
        dataIndex: 'enabled',
        width: 90,
        render: (v, row) => (
          <Switch
            checked={!!v}
            onChange={() => handleToggle(row)}
            disabled={!isAdmin || togglingId === row.id}
            loading={togglingId === row.id}
          />
        ),
      },
      {
        title: '创建时间',
        dataIndex: 'created_at',
        width: 160,
        render: (v) => (v ? dayjs(v).format('YYYY-MM-DD HH:mm') : '-'),
      },
      {
        title: '操作',
        width: 140,
        fixed: 'right',
        render: (_, row) => (
          <div className="flex items-center gap-1">
            {isAdmin && (
              <>
                <Button
                  size="small"
                  theme="borderless"
                  icon={<Pencil size={14} />}
                  onClick={() => openEdit(row)}
                >
                  编辑
                </Button>
                <Button
                  size="small"
                  theme="borderless"
                  type="danger"
                  icon={<Trash2 size={14} />}
                  onClick={() => handleDelete(row)}
                />
              </>
            )}
            {!isAdmin && (
              <Button
                size="small"
                theme="borderless"
                icon={<Boxes size={14} />}
                onClick={() => openEdit(row)}
              >
                查看
              </Button>
            )}
          </div>
        ),
      },
    ]

  // 把 formValues 展平为 Semi Form 的 initValues（点号字段）
  const initValues = useMemo(() => {
    return {
      name: formValues.name,
      description: formValues.description,
      'main_text_model.base_url': formValues.main_text_model?.base_url || '',
      'main_text_model.api_key': formValues.main_text_model?.api_key || '',
      'main_text_model.model': formValues.main_text_model?.model || '',
      'main_text_model.api_type':
        formValues.main_text_model?.api_type || 'openai',
      'main_text_model.extra_url':
        formValues.main_text_model?.extra_url || '',
      'main_text_model.max_retries':
        formValues.main_text_model?.max_retries ?? 1,
      'main_text_model.weight': formValues.main_text_model?.weight ?? 1,
      image_strategy: formValues.image_strategy || 'round_robin',
      image_prompt: formValues.image_prompt || '',
      replace_image: !!formValues.replace_image,
      enabled: !!formValues.enabled,
    }
  }, [formValues])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">模型组管理</h2>
          <p className="text-sm text-semi-color-text-2 mt-1">
            管理对话+图片模型组、上游配置与启用状态。
          </p>
        </div>
        {isAdmin && (
          <Button
            theme="solid"
            className="!rounded-full"
            icon={<Plus size={16} />}
            onClick={openCreate}
          >
            新建模型组
          </Button>
        )}
      </div>

      <CardPro
        title="模型组列表"
        extra={
          <div
            className="flex items-center gap-2 px-4 py-1.5 !rounded-full"
            style={{
              border: '1px solid var(--semi-color-border)',
              background: 'var(--semi-color-bg-0)',
            }}
          >
            <Search size={14} style={{ color: 'var(--semi-color-text-2)' }} />
            <input
              className="text-sm bg-transparent outline-none"
              style={{
                color: 'var(--semi-color-text-0)',
                minWidth: 180,
              }}
              placeholder="搜索名称 / 描述"
              value={keywordInput}
              onChange={(e) => {
                setKeywordInput(e.target.value)
              }}
            />
          </div>
        }
        footer={
          <div className="flex items-center justify-between">
            <span className="text-sm text-semi-color-text-2">
              共 {total} 个
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
        <Table
          columns={columns}
          dataSource={data}
          rowKey="id"
          loading={loading}
          pagination={false}
          scroll={{ x: 1200 }}
          empty={<Empty title="暂无模型组" description="点击右上角「新建模型组」开始" />}
        />
      </CardPro>

      {/* 新建/编辑 Modal */}
      <Modal
        title={editingId ? '编辑模型组' : '新建模型组'}
        visible={modalVisible}
        onCancel={() => setModalVisible(false)}
        footer={null}
        width={720}
        maskClosable={false}
      >
        {modalLoading ? (
          <div className="flex justify-center py-12">
            <Spin size="large" />
          </div>
        ) : (
          <Form
            key={editingId || 'new'}
            initValues={initValues}
            onSubmit={handleSubmit}
            labelPosition="top"
          >
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <Form.Input
                field="name"
                label="名称"
                placeholder="如 gpt-4o-image"
                rules={[{ required: true, message: '请输入名称' }]}
              />
              <Form.Input
                field="description"
                label="描述"
                placeholder="可选"
              />
            </div>

            <Divider margin={12}>主对话模型</Divider>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <Form.Input
                field="main_text_model.base_url"
                label="Base URL"
                placeholder="https://api.openai.com/v1"
                rules={[{ required: true, message: '请输入 Base URL' }]}
              />
              <Form.Input
                field="main_text_model.api_key"
                label="API Key"
                placeholder="sk-..."
                mode="password"
                rules={[{ required: true, message: '请输入 API Key' }]}
              />
              <Form.Input
                field="main_text_model.model"
                label="模型"
                placeholder="如 gpt-4o"
                rules={[{ required: true, message: '请输入模型名' }]}
              />
              <Form.Input
                field="main_text_model.api_type"
                label="API 类型"
                placeholder="openai"
                initValue="openai"
              />
              <Form.InputNumber
                field="main_text_model.max_retries"
                label="最大重试"
                initValue={1}
                min={0}
              />
              <Form.InputNumber
                field="main_text_model.weight"
                label="权重"
                initValue={1}
                min={1}
              />
              <Form.Input
                field="main_text_model.extra_url"
                label="附加 URL（可选）"
                placeholder="可选"
              />
            </div>

            <Divider margin={12}>图片模型（至少 1 个）</Divider>
            <div className="space-y-4">
              {imageModels.map((m, idx) => (
                <div
                  key={m._uid}
                  className="!rounded-2xl p-4"
                  style={{
                    border: imageModelsErrors[idx] ? '1px solid var(--semi-color-danger)' : '1px solid var(--semi-color-border)',
                    background: 'var(--semi-color-bg-1)',
                  }}
                >
                  <div className="flex items-center justify-between mb-3">
                    <span className="text-sm font-semibold">
                      图片模型 #{idx + 1}
                      {imageModelsErrors[idx] && (
                        <span style={{ color: 'var(--semi-color-danger)', fontSize: 12, marginLeft: 8 }}>
                          请填写必填项
                        </span>
                      )}
                    </span>
                    <Button
                      size="small"
                      theme="borderless"
                      type="danger"
                      icon={<Trash2 size={14} />}
                      onClick={() => removeImageModel(idx)}
                    >
                      移除
                    </Button>
                  </div>
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                    <Form.Input
                      field={`__img_${idx}_base_url`}
                      label="Base URL"
                      noLabel
                      placeholder="Base URL"
                      value={m.base_url}
                      validateStatus={imageModelsErrors[idx]?.base_url ? 'error' : undefined}
                      onChange={(v) => updateImageModel(idx, 'base_url', v)}
                    />
                    <Form.Input
                      field={`__img_${idx}_api_key`}
                      label="API Key"
                      noLabel
                      placeholder="API Key"
                      mode="password"
                      value={m.api_key}
                      validateStatus={imageModelsErrors[idx]?.api_key ? 'error' : undefined}
                      onChange={(v) => updateImageModel(idx, 'api_key', v)}
                    />
                    <Form.Input
                      field={`__img_${idx}_model`}
                      label="模型"
                      noLabel
                      placeholder="模型，如 gpt-image-1"
                      value={m.model}
                      validateStatus={imageModelsErrors[idx]?.model ? 'error' : undefined}
                      onChange={(v) => updateImageModel(idx, 'model', v)}
                    />
                    <Form.Input
                      field={`__img_${idx}_api_type`}
                      label="API 类型"
                      noLabel
                      placeholder="API 类型，默认 openai"
                      value={m.api_type}
                      onChange={(v) => updateImageModel(idx, 'api_type', v)}
                    />
                  </div>
                </div>
              ))}
              <Button
                theme="light"
                className="!rounded-full"
                icon={<Plus size={14} />}
                onClick={addImageModel}
              >
                添加图片模型
              </Button>
            </div>

            <Divider margin={12}>其他设置</Divider>
            <div className="space-y-4">
              <Form.RadioGroup
                field="image_strategy"
                label="图片策略"
                type="button"
              >
                <Radio value="round_robin">轮询</Radio>
                <Radio value="failover">故障转移</Radio>
              </Form.RadioGroup>

              <Form.TextArea
                field="image_prompt"
                label="图片识别提示词"
                placeholder="用于识别用户是否需要图片"
                rows={3}
              />

              <div className="flex items-center gap-6">
                <Form.Switch
                  field="replace_image"
                  label="替换图片"
                />
                <Form.Switch
                  field="enabled"
                  label="启用"
                />
              </div>
            </div>

            <div className="flex justify-end gap-2 mt-6">
              <Button
                onClick={() => setModalVisible(false)}
                className="!rounded-full"
              >
                取消
              </Button>
              <Button
                htmlType="submit"
                theme="solid"
                loading={modalLoading}
                className="!rounded-full"
              >
                保存
              </Button>
            </div>
          </Form>
        )}
      </Modal>
    </div>
  )
}
