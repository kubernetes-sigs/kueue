import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000, // Same port as react-scripts for consistency
    open: true,
  },
  build: {
    outDir: 'build', // Same output directory as react-scripts
    sourcemap: true,
  },
  resolve: {
    extensions: ['.js', '.jsx', '.json'],
  },
  publicDir: 'public',
  envPrefix: ['VITE_', 'REACT_APP_'], // Support both VITE_ and REACT_APP_ prefixes
}); 