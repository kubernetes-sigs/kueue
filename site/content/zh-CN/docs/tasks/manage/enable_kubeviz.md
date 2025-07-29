---
title: "启用 Dashboard（KueueViz）"
date: 2025-07-18
weight: 12
description: >
  安装和配置 KueueViz，这是一个基于 Web 的 Kueue 工作负载监控可视化工具。
---

{{% alert title="注意" color="primary" %}}
为了简化安装和升级过程，我们建议使用 Helm 部署 Kueue。  
你可以使用本地 `values.yaml` 文件来自定义部署以适应你的环境。  
有关完整说明，请参阅 [Helm chart 安装指南](/zh-CN/docs/installation/#install-by-helm)。
{{% /alert %}}

KueueViz 是一个基于 Web 的可视化工具，提供对 Kueue 工作负载、队列和资源分配的实时监控。
它提供了直观的 Dashboard，用于观察作业队列状态、资源利用率和工作负载进展。

本页面展示如何在你的集群中安装和配置 KueueViz。

本页面的目标读者是[批处理管理员](/zh-CN/docs/tasks#batch-administrator)。

## 开始之前 {#before-you-begin}

请确保满足以下条件：

- Kubernetes 集群正在运行
- kubectl 命令行工具可以与你的集群通信
- 已安装 Helm 命令行工具
- 你的集群中已安装 Kueue
- （可选）用于外部访问的入口控制器（例如 Nginx Ingress Controller）

KueueViz 可以使用 Helm（推荐）或 kubectl 进行安装。
选择最适合你工作流程的方法。

## 与 Kueue 一起安装 {#install-with-kueue}

在安装新的 Kueue 时一起安装 KueueViz：

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --create-namespace \
  --set enableKueueViz=true \ # 启用 KueueViz
  --wait --timeout 300s
```

有关安装 Kueue 的更多信息，请参阅[安装](/zh-CN/docs/installation)。

## 启用 KueueViz（安装 Kueue 之后）{#enable-kueueviz-kueue-is-already-installed}

如果 Kueue 已经安装，你可以通过 Helm 或 kubectl 启用 KueueViz。

### 通过 Helm 启用 KueueViz {#enable-kueueviz-by-helm}

在现有的 Helm 安装的 Kueue 上启用 KueueViz：

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --set enableKueueViz=true # 启用 KueueViz
```

### 通过 YAML 启用 KueueViz {#enable-kueueviz-by-yaml}

在现有的 Kueue 安装上通过 YAML 启用 KueueViz：

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## 访问 Dashboard {#accessing-the-dashboard}

### 端口转发（仅用于开发） {#port-forwarding-only-for-development}

在开发或测试期间快速访问（已在 Docker Desktop 上测试）：

```bash
kubectl port-forward svc/kueue-kueue-viz-frontend -n kueue-system 8080
kubectl port-forward svc/kueue-kueue-viz-backend  -n kueue-system 8081:8080
```

编辑 kueue-viz-frontend Deployment 以设置环境变量
`REACT_APP_WEBSOCKET_URL=ws://localhost:8081`。

然后在 [http://localhost:8080](http://localhost:8080) 访问 Dashboard。

### Ingress {#ingress}

对于生产部署，配置 Ingress 资源：

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kueueviz-ingress
  namespace: kueue-system
spec:
  rules:
    - host: kueueviz.example.com
      http:
        paths:
          - path: /api(/|$)(.*)
            pathType: Prefix
            backend:
              service:
                name: kueue-kueueviz-backend
                port:
                  number: 8080
          - path: /
            pathType: Prefix
            backend:
              service:
                name: kueue-kueueviz-frontend
                port:
                  number: 8080
  tls:
    - hosts:
        - kueueviz.example.com # 替换为你的域名
      secretName: kueueviz-tls # 你需要首先创建 TLS 密钥
```

### LoadBalancer {#loadbalancer}

对于支持 LoadBalancer 的云环境：

```yaml
apiVersion: v1
kind: Service
metadata:
  name: kueueviz-loadbalancer
  namespace: kueue-system
spec:
  type: LoadBalancer
  ports:
    - name: http
      port: 80
      targetPort: 8080
      protocol: TCP
  selector:
    app.kubernetes.io/name: kueue
    app.kubernetes.io/component: kueueviz-frontend
```

## 升级 {#upgrade}

### 通过 Helm 升级 {#upgrade-by-helm}

通过 Helm 升级 KueueViz：

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --set enableKueueViz=true
```

### 通过 YAML 升级 {#upgrade-by-yaml}

通过 YAML 升级 KueueViz：

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## 卸载 {#uninstall}

**注意：** 请确保卸载的是 KueueViz，而不是意外卸载 Kueue。

卸载 KueueViz 组件：

```bash
kubectl delete -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## 下一步 {#whats-next}

- 探索更多[任务](/zh-CN/docs/tasks)
- 了解[概念](/zh-CN/docs/concepts)
