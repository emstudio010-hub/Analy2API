/** @type {import('tailwindcss').Config} */
// 语义化颜色令牌：所有组件统一引用语义令牌，深浅双主题仅切换 CSS 变量。
// 变量以 "R G B" 通道值存放，支持 `bg-accent/10` 等透明度修饰符。
const token = (name) => `rgb(var(--c-${name}) / <alpha-value>)`

export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{vue,ts,tsx,js}'],
  theme: {
    extend: {
      colors: {
        // 表面层级：底布 → 面板 → 抬升控件面 → 悬浮/高亮层
        canvas: token('canvas'),
        surface: token('surface'),
        raised: token('raised'),
        overlay: token('overlay'),

        // 精密描边层级
        line: {
          DEFAULT: token('line'),
          strong: token('line-strong'),
        },

        // 文字对比层级（均满足 ≥4.5:1 可读性底线）
        fg: token('fg'),
        muted: token('muted'),
        subtle: token('subtle'),

        // 工业信号色（零霓虹紫渐变）
        accent: {
          DEFAULT: token('accent'),
          fg: token('accent-fg'),
        },
        success: token('success'),
        warning: token('warning'),
        danger: token('danger'),
        info: token('info'),
      },
      borderColor: {
        DEFAULT: token('line'),
      },
      divideColor: {
        DEFAULT: token('line'),
      },
      ringColor: {
        DEFAULT: token('accent'),
      },
      fontFamily: {
        sans: [
          'Inter',
          '-apple-system',
          'BlinkMacSystemFont',
          'Segoe UI',
          'PingFang SC',
          'Hiragino Sans GB',
          'Microsoft YaHei',
          'Noto Sans SC',
          'Roboto',
          'Helvetica Neue',
          'Arial',
          'sans-serif',
        ],
        mono: [
          'JetBrains Mono',
          'Cascadia Mono',
          'SFMono-Regular',
          'Menlo',
          'Consolas',
          'Liberation Mono',
          'monospace',
        ],
      },
      fontSize: {
        '2xs': ['0.6875rem', { lineHeight: '1rem' }],
      },
      screens: {
        nav: '960px',
      },
      borderRadius: {
        panel: '0.5rem',
      },
      boxShadow: {
        panel: 'var(--shadow-panel)',
        pop: 'var(--shadow-pop)',
      },
      transitionDuration: {
        DEFAULT: '140ms',
      },
    },
  },
  plugins: [],
}
