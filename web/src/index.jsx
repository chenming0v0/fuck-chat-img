// React 19 兼容适配器必须最先导入
import '@douyinfe/semi-ui/react19-adapter'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import '@douyinfe/semi-ui/dist/css/semi.css'
import App from './App'
import './index.css'

// 提示统一使用 Semi UI 的 Toast(静态方法 Toast.error/success/warning),
// 它自带挂载容器, 无需像 react-toastify 那样手动放 ToastContainer,
// /login /setup 等独立路由(不经 PageLayout)也能正常显示提示.
ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </React.StrictMode>,
)
