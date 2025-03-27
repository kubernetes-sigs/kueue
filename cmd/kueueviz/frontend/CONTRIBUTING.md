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

