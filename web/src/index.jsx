// React 19 兼容适配器必须最先导入
import '@douyinfe/semi-ui/react19-adapter'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import '@douyinfe/semi-ui/dist/css/semi.css'
import 'react-toastify/dist/ReactToastify.css'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </React.StrictMode>,
)
