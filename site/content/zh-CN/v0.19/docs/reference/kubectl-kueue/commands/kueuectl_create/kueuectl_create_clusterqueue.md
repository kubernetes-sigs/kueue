---
title: kueuectl create clusterqueue
content_type: tool-reference
auto_generated: true
no_list: false
---

## 概要 {#synopsis}


使用给定名称创建 ClusterQueue。

```
kueuectl create clusterqueue NAME [--cohort COHORT_NAME] [--queuing-strategy QUEUEING_STRATEGY] [--namespace-selector KEY=VALUE] [--reclaim-within-cohort PREEMPTION_POLICY] [--preemption-within-cluster-queue PREEMPTION_POLICY] [--nominal-quota RESOURCE_FLAVOR:RESOURCE=VALUE] [--borrowing-limit RESOURCE_FLAVOR:RESOURCE=VALUE] [--lending-limit RESOURCE_FLAVOR:RESOURCE=VALUE] [--dry-run STRATEGY]
```


## 示例 {#examples}

```
  # 创建 ClusterQueue
  kueuectl create clusterqueue my-cluster-queue
  
  # 创建具有队列组、命名空间选择器和其他详细信息的 ClusterQueue
  kueuectl create clusterqueue my-cluster-queue \
  --cohort cohortname \
  --queuing-strategy StrictFIFO \
  --namespace-selector fooX=barX,fooY=barY \
  --reclaim-within-cohort Any \
  --preemption-within-cluster-queue LowerPriority
  
  # 创建具有标称配额和一个名为 alpha 的资源类型的 ClusterQueue
  kueuectl create clusterqueue my-cluster-queue --nominal-quota "alpha:cpu=9;memory=36Gi"
  
  # 创建具有多个资源类型（名为 alpha 和 beta）的 ClusterQueue
  kueuectl create clusterqueue my-cluster-queue \
  --cohort cohortname \
  --nominal-quota "alpha:cpu=9;memory=36Gi;nvidia.com/gpu=10,beta:cpu=18;memory=72Gi;nvidia.com/gpu=20" \
  --borrowing-limit "alpha:cpu=1;memory=1Gi;nvidia.com/gpu=1,beta:cpu=2;memory=2Gi;nvidia.com/gpu=2" \
  --lending-limit "alpha:cpu=1;memory=1Gi;nvidia.com/gpu=1,beta:cpu=2;memory=2Gi;nvidia.com/gpu=2"
```


## 选项 {#options}


<table style="width: 100%; table-layout: fixed;">
    <colgroup>
        <col span="1" style="width: 10px;" />
        <col span="1" />
    </colgroup>
    <tbody>
    <tr>
        <td colspan="2">--allow-missing-template-keys&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: true</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，当模板中缺少字段或映射键时忽略模板中的任何错误。仅适用于 golang 和 jsonpath 输出格式。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--borrowing-limit strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>此 ClusterQueue 允许从同一队列组中其他 ClusterQueue 的未使用配额中借用的[类型，资源]组合的最大配额量。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--cohort string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>此 ClusterQueue 所属的队列组。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-h, --help</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>clusterqueue 的帮助信息</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--lending-limit strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>此 ClusterQueue 可以借给同一队列组中其他 ClusterQueue 的[类型，资源]组合的未使用配额的最大量。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--namespace-selector &lt;comma-separated &#39;key=value&#39; pairs&gt;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: []</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>定义允许向此 ClusterQueue 提交工作负载的命名空间。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--nominal-quota strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>在某个时间点此 ClusterQueue 接纳的工作负载可用的此资源的数量。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-o, --output string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>输出格式。可选值为：json、yaml、name、go-template、go-template-file、template、templatefile、jsonpath、jsonpath-as-json、jsonpath-file。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--preemption-within-cluster-queue string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>确定不符合其 ClusterQueue 标称配额的待处理工作负载是否可以抢占 ClusterQueue 中的活动工作负载。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--queuing-strategy string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>此 ClusterQueue 中队列间工作负载的排队策略。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--reclaim-within-cohort string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>确定待处理工作负载是否可以抢占队列组中使用超过其标称配额的其他 ClusterQueue 中的工作负载。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--show-managed-fields</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，在 JSON 或 YAML 格式打印对象时保留 managedFields。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--template string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>当-o=go-template、-o=go-template-file 时使用的模板字符串或模板文件路径。模板格式是 golang 模板 [http://golang.org/pkg/text/template/#pkg-overview]。</p>
        </td>
    </tr>
    </tbody>
</table>



## 从父命令继承的选项 {#options-inherited-from-parent-commands}
<table style="width: 100%; table-layout: fixed;">
    <colgroup>
        <col span="1" style="width: 10px;" />
        <col span="1" />
    </colgroup>
    <tbody>
    <tr>
        <td colspan="2">--as string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>为操作模拟的用户名。用户可以是常规用户或命名空间中的服务账户。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-group strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>为操作模拟的组，此标志可以重复指定多个组。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-uid string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>为操作模拟的 UID。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--cache-dir string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;$HOME/.kube/cache&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>默认缓存目录</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--certificate-authority string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>证书颁发机构证书文件的路径</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--client-certificate string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>TLS 客户端证书文件的路径</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--client-key string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>TLS 客户端密钥文件的路径</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--cluster string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>要使用的 kubeconfig 集群的名称</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--context string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>要使用的 kubeconfig 上下文的名称</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--disable-compression</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，则选择退出对所有服务器请求的响应压缩</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--dry-run string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;none&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>必须是 &#34;none&#34;、&#34;server&#34; 或 &#34;client&#34;。如果是客户端策略，只打印将要发送的对象，而不发送它。如果是服务器策略，提交服务器端请求而不持久化资源。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--insecure-skip-tls-verify</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，将不会检查服务器证书的有效性。这将使您的 HTTPS 连接不安全</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--kubeconfig string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>用于 CLI 请求的 kubeconfig 文件的路径。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-n, --namespace string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果存在，此 CLI 请求的命名空间范围</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--request-timeout string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;0&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>在放弃单个服务器请求之前等待的时间长度。非零值应包含相应的时间单位（例如 1s、2m、3h）。零值表示不超时请求。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-s, --server string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Kubernetes API 服务器的地址和端口</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--tls-server-name string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>用于服务器证书验证的服务器名称。如果未提供，则使用用于联系服务器的主机名</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--token string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>用于 API 服务器身份验证的持有者令牌</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--user string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>要使用的 kubeconfig 用户的名称</p>
        </td>
    </tr>
    </tbody>
</table>



## 另请参阅 {#see-also}

* [kueuectl create](../) - 创建资源

