import React, { useEffect, useMemo, useState } from 'react'
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
} from '@douyinfe/semi-ui'
import { Plus, Search, Pencil, Trash2, Boxes } from 'lucide-react'
import dayjs from 'dayjs'
import { toast } from 'react-toastify'
import CardPro from '@/components/common/ui/CardPro'
import {
  listGroups,
  getGroupPlain,
  createGroup,
  updateGroup,
  deleteGroup,
  toggleGroup,
} from '@/helpers/api'
import { useAuth } from '@/helpers/auth'

// 默认上游模型对象
function defaultUpstream() {
  return {
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
  const [keyword, setKeyword] = useState('')

  const [modalVisible, setModalVisible] = useState(false)
  const [modalLoading, setModalLoading] = useState(false)
  const [editingId, setEditingId] = useState(null)
  const [formValues, setFormValues] = useState(DEFAULT_FORM)
  // 手动管理图片模型数组（与 Form 同步）
  const [imageModels, setImageModels] = useState([defaultUpstream()])

  async function refresh() {
    setLoading(true)
    try {
      const res = await listGroups({
        p: page,
        size,
        keyword: keyword || undefined,
      })
      if (res?.success) {
        setData(res.data || [])
        setTotal(res.total || 0)
      }
    } catch (e) {
      // 静默
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, size, keyword])

  // 打开新建 Modal
  function openCreate() {
    setEditingId(null)
    setFormValues(DEFAULT_FORM)
    setImageModels([defaultUpstream()])
    setModalVisible(true)
  }

  // 打开编辑 Modal：先拉详情获取明文 api_key
  async function openEdit(row) {
    setEditingId(row.id)
    setModalVisible(true)
    setModalLoading(true)
    try {
      const res = await getGroupPlain(row.id)
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
        const imgs = (g.image_models || []).map((m) => ({
          base_url: m.base_url || '',
          api_key: m.api_key || '',
          model: m.model || '',
          api_type: m.api_type || 'openai',
          extra_url: m.extra_url || '',
          max_retries: m.max_retries != null ? m.max_retries : 1,
          weight: m.weight != null ? m.weight : 1,
        }))
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
      } else {
        toast.error(res?.message || '加载详情失败')
        setModalVisible(false)
      }
    } catch (e) {
      toast.error(e?.response?.data?.message || '加载详情失败')
      setModalVisible(false)
    } finally {
      setModalLoading(false)
    }
  }

  // 切换启用状态：直接调 toggle
  async function handleToggle(row) {
    try {
      const res = await toggleGroup(row.id)
      if (res?.success) {
        toast.success(res.data?.enabled ? '已启用' : '已停用')
        refresh()
      } else {
        toast.error(res?.message || '切换失败')
      }
    } catch (e) {
      toast.error(e?.response?.data?.message || '切换失败')
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
            toast.success('已删除')
            refresh()
          } else {
            toast.error(res?.message || '删除失败')
          }
        } catch (e) {
          toast.error(e?.response?.data?.message || '删除失败')
        }
      },
    })
  }

  // 图片模型数组操作
  function addImageModel() {
    setImageModels((prev) => [...prev, defaultUpstream()])
  }
  function removeImageModel(idx) {
    setImageModels((prev) => {
      if (prev.length <= 1) {
        toast.warn('至少需要 1 个图片模型')
        return prev
      }
      return prev.filter((_, i) => i !== idx)
    })
  }
  function updateImageModel(idx, key, value) {
    setImageModels((prev) =>
      prev.map((m, i) => (i === idx ? { ...m, [key]: value } : m)),
    )
  }

  // 提交表单
  async function handleSubmit(values) {
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
      toast.error('请填写名称')
      return
    }
    if (!payload.image_models.length) {
      toast.error('至少需要 1 个图片模型')
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
      if (res?.success) {
        toast.success(editingId ? '已更新' : '已创建')
        setModalVisible(false)
        refresh()
      } else {
        toast.error(res?.message || '保存失败')
      }
    } catch (e) {
      toast.error(e?.response?.data?.message || '保存失败')
    } finally {
      setModalLoading(false)
    }
  }

  const columns = useMemo(
    () => [
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
            disabled={!isAdmin}
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
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [isAdmin],
  )

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
              value={keyword}
              onChange={(e) => {
                setKeyword(e.target.value)
                setPage(1)
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
              />
              <Form.Input
                field="main_text_model.api_key"
                label="API Key"
                placeholder="sk-..."
                mode="password"
              />
              <Form.Input
                field="main_text_model.model"
                label="模型"
                placeholder="如 gpt-4o"
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
                  key={idx}
                  className="!rounded-2xl p-4"
                  style={{
                    border: '1px solid var(--semi-color-border)',
                    background: 'var(--semi-color-bg-1)',
                  }}
                >
                  <div className="flex items-center justify-between mb-3">
                    <span className="text-sm font-semibold">
                      图片模型 #{idx + 1}
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
                      onChange={(v) => updateImageModel(idx, 'base_url', v)}
                    />
                    <Form.Input
                      field={`__img_${idx}_api_key`}
                      label="API Key"
                      noLabel
                      placeholder="API Key"
                      mode="password"
                      value={m.api_key}
                      onChange={(v) => updateImageModel(idx, 'api_key', v)}
                    />
                    <Form.Input
                      field={`__img_${idx}_model`}
                      label="模型"
                      noLabel
                      placeholder="模型，如 gpt-image-1"
                      value={m.model}
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
