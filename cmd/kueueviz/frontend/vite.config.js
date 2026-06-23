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
    alias: {
      // MUI 9.1.1 imports react-transition-group as a directory which is not
      // supported in ES modules. Redirect to the CJS version for Vitest compatibility.
      'react-transition-group/TransitionGroupContext':
        'react-transition-group/cjs/TransitionGroupContext.js',
    },
  },
  publicDir: 'public',
  envPrefix: ['VITE_', 'REACT_APP_'], // Support both VITE_ and REACT_APP_ prefixes
  test: {
    environment: 'node',
    server: {
      deps: {
        // @mui/material 9.1.1 imports react-transition-group using a directory path
        // which is not supported in ES modules. Inlining both packages forces Vitest
        // to transform them rather than passing them to Node's native ESM resolver.
        inline: ['@mui/material', 'react-transition-group'],
      },
    },
  },
}); 