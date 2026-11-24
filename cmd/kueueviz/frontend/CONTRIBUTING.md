# Contribution guide


## Local development

Edit `.env` file to match the address of the backend.

```
REACT_APP_WEBSOCKET_URL=wss://localhost:8080
```

Download dependencies and build the frontend:
```
make build
```

Start npm in dev mode:
```
make debug
```

This will open your browser and enables hot code replacement on the client side.

## Running tests

Run unit tests:
```
make test
```

Run unit tests with a coverage report (printed to the terminal and written to
`coverage/index.html`):
```
make test-coverage
```

### Writing tests

Component tests live next to the component, as `<Component>.test.jsx`, and use
[Vitest](https://vitest.dev/) with [Testing Library](https://testing-library.com/docs/react-testing-library/intro/)
and the `jsdom` environment. Tests that do not render anything can stay as
`.test.js`. Vite only parses JSX in `.jsx` files, so use that extension for
every test containing JSX.

- Start the file with `// @vitest-environment jsdom`. The default environment
  is `node`.
- Mock `useWebSocket` instead of opening a connection, and use the helpers in
  `src/testing/helpers.jsx` (`mockWebSocket`, `renderWithRouter`, `renderAtRoute`).
  Add `vi.mock('./useWebSocket', () => ({ default: vi.fn() }))` to the test file,
  since `vi.mock` is hoisted per file.
- Mock `ViewYamlButton` in page tests. It needs an `AuthProvider` and has its own tests.
- Prefer queries by role and visible text (`getByRole`, `getByText`) over
  class names, and add `afterEach(cleanup)`.
- Cover the loading, error and empty states of a component, not only the happy path.
