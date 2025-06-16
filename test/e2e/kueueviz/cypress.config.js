import { defineConfig } from "cypress";

export default defineConfig({
  e2e: {
    baseUrl: 'http://localhost:3000',
    screenshotsFolder: process.env.CYPRESS_SCREENSHOTS_FOLDER || 'cypress/screenshots',
    video: true,
    videosFolder: process.env.CYPRESS_VIDEOS_FOLDER || 'cypress/videos',
    specPattern: 'cypress/e2e/**/*.cy.{js,ts}',
    supportFile: false,
    setupNodeEvents(on, config) {
    },
  },

  component: {
    devServer: {
      framework: 'react',
      bundler: 'vite',
    },
  },
});
