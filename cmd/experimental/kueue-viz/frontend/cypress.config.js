const { defineConfig } = require("cypress");

module.exports = defineConfig({
  e2e: {
    "baseUrl": "https://frontend.kueue-viz.local",
    hosts: {
      "frontend.kueue-viz.local": "127.0.0.1",
      "backend.kueue-viz.local": "127.0.0.1"
    },
    setupNodeEvents(on, config) {
      // implement node event listeners here
    },
  },
});
