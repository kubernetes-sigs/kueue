# Contribution guide

## Local development

Run the backend locally using in debug mode. Automatic code reload is enabled.

```
make debug
```

## Local run

Run the backend locally using release mode. Logging is only set for fatal and error messages.

```
make run
```

## Local container run

```
podman build . -t backend
podman run -v $HOME/.kube:/nonexistent/.kube/ -p 8080:8080 backend
```

