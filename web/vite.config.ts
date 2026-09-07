import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

const outDir = '../internal/panel/ui/dist';

// emptyOutDir 会清掉 .gitkeep；构建完补回，保证未构建时 go:embed 目录仍存在（vite 在 web/ 下运行）。
const keepGitkeep = (): Plugin => ({
  name: 'keep-gitkeep',
  closeBundle() {
    writeFileSync(resolve(outDir, '.gitkeep'), '');
  },
});

// 构建产物直接输出到 Go 内嵌目录 internal/panel/ui/dist（见 Makefile `ui`）。
export default defineConfig({
  plugins: [react(), keepGitkeep()],
  build: {
    outDir,
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true, ws: true },
    },
  },
});
