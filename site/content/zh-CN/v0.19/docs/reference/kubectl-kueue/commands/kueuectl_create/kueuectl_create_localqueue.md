---
title: kueuectl create localqueue
content_type: tool-reference
auto_generated: true
no_list: false
---

## 概要 {#synopsis}


在指定命名空间中使用给定名称创建本地队列。

```
kueuectl create localqueue NAME -c CLUSTER_QUEUE_NAME [--ignore-unknown-cq] [--dry-run STRATEGY]
```


## 示例 {#examples}

```
  # 创建本地队列
  kueuectl create localqueue my-local-queue -c my-cluster-queue
  
  # 创建具有未知集群队列的本地队列
  kueuectl create localqueue my-local-queue -c my-cluster-queue -i
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
        <td colspan="2">-c, --clusterqueue string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>将与本地队列关联的集群队列名称（必需）。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-h, --help</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>localqueue 的帮助信息</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-i, --ignore-unknown-cq</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>忽略未知的集群队列。</p>
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

