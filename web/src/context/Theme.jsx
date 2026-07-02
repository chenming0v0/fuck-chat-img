import React, { createContext, useContext, useEffect, useState } from 'react'

const ThemeContext = createContext(null)

// 主题上下文：light / dark
// dark：html 加 .dark class + theme-mode="dark"(Semi UI CSS 变量也挂在此)
export function ThemeProvider({ children }) {
  const [theme, setTheme] = useState(
    () => localStorage.getItem('fci_theme') || 'light',
  )

  useEffect(() => {
    const html = document.documentElement
    if (theme === 'dark') {
      html.classList.add('dark')
      html.setAttribute('theme-mode', 'dark')
    } else {
      html.classList.remove('dark')
      html.removeAttribute('theme-mode')
    }
    localStorage.setItem('fci_theme', theme)
  }, [theme])

  function toggleTheme() {
    setTheme((prev) => (prev === 'dark' ? 'light' : 'dark'))
  }

  const value = {
    theme,
    isDark: theme === 'dark',
    setTheme,
    toggleTheme,
  }

  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) {
    throw new Error('useTheme 必须在 ThemeProvider 内使用')
  }
  return ctx
}
