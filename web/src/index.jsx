// React 19 兼容适配器必须最先导入
import '@douyinfe/semi-ui/react19-adapter'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { ToastContainer } from 'react-toastify'
import '@douyinfe/semi-ui/dist/css/semi.css'
import 'react-toastify/dist/ReactToastify.css'
import App from './App'
import './index.css'

// ToastContainer 挂在最外层, 保证 /login /setup 等独立路由(不经 PageLayout)也能显示提示.
// 此前仅挂在 PageLayout 内, 导致 Setup/Login 页所有 toast 静默失效.
ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
      <ToastContainer position="top-center" autoClose={3000} />
    </BrowserRouter>
  </React.StrictMode>,
)
