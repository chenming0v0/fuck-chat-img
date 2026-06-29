import React, { createContext, useContext, useEffect, useState } from 'react'

const ThemeContext = createContext(null)

// 主题上下文：light / dark
// dark：html 加 .dark class，body 加 theme-mode="dark"
export function ThemeProvider({ children }) {
  const [theme, setTheme] = useState(
    () => localStorage.getItem('fci_theme') || 'light',
  )

  useEffect(() => {
    const html = document.documentElement
    const body = document.body
    if (theme === 'dark') {
      html.classList.add('dark')
      body.setAttribute('theme-mode', 'dark')
    } else {
      html.classList.remove('dark')
      body.removeAttribute('theme-mode')
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
