import path from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// 开发态把 /api 代理到本机 rigd，前端不用关心跨域与端口。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // shadcn 生成的组件用 @/ 别名互相引用，这里必须和 tsconfig 的 paths 对上
  resolve: { alias: { '@': path.resolve(__dirname, './src') } },
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': 'http://127.0.0.1:8088' },
  },
});
