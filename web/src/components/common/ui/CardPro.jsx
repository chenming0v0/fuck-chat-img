import React from 'react'
import { Card } from '@douyinfe/semi-ui'

// 统一卡片外壳：圆角 2xl，title / extra / children / footer
export default function CardPro({
  title,
  extra,
  footer,
  children,
  className = '',
  bodyClassName = '',
  ...rest
}) {
  return (
    <Card
      className={`!rounded-2xl ${className}`}
      title={title}
      extra={extra}
      footer={footer}
      footerStyle={{ padding: '12px 16px' }}
      headerStyle={{ padding: '16px 20px' }}
      bodyStyle={{ padding: '16px 20px' }}
      {...rest}
    >
      <div className={bodyClassName}>{children}</div>
    </Card>
  )
}
