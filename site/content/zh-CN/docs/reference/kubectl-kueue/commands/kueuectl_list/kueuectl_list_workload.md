---
title: kueuectl list workload
content_type: tool-reference
auto_generated: true
no_list: false
---

## 概要 {#synopsis}


列出符合提供条件的工作负载。

```
kueuectl list workload [--clusterqueue CLUSTER_QUEUE_NAME] [--localqueue LOCAL_QUEUE_NAME] [--status STATUS] [--selector key1=value1] [--field-selector key1=value1] [--all-namespaces] [--for TYPE[.API-GROUP]/NAME]
```


## 示例 {#examples}

```
  # 列出工作负载
  kueuectl list workload
```


## 选项 {#options}


<table style="width: 100%; table-layout: fixed;">
    <colgroup>
        <col span="1" style="width: 10px;" />
        <col span="1" />
    </colgroup>
    <tbody>
    <tr>
        <td colspan="2">-A, --all-namespaces</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果存在，则列出所有命名空间中的请求对象。即使使用 --namespace 指定，当前上下文中的命名空间也会被忽略。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--allow-missing-template-keys&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;默认值: true</td>
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
            <p>按与资源关联的集群队列名称过滤。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--field-selector string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>用于过滤的选择器（字段查询），支持 &#39;=&#39;、&#39;==&#39; 和 &#39;!=&#39;。（例如 --field-selector key1=value1,key2=value2）。服务器每种类型仅支持有限数量的字段查询。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--for string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>仅过滤与指定资源相关的那些。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-h, --help</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>workload 命令的帮助信息</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-q, --localqueue string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>按与资源关联的本地队列名称过滤。</p>
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
        <td colspan="2">-l, --selector string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>用于过滤的选择器（标签查询），支持 &#39;=&#39;、&#39;==&#39; 和 &#39;!=&#39;。（例如 -l key1=value1,key2=value2）。匹配的对象必须满足所有指定的标签约束。</p>
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
        <td colspan="2">--status strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>按状态过滤工作负载。必须是 &#34;all&#34;、&#34;pending&#34;、&#34;admitted&#34; 或 &#34;finished&#34;</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--template string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>当 -o=go-template, -o=go-template-file 时使用的模板字符串或模板文件路径。模板格式是 golang 模板 [http://golang.org/pkg/text/template/#pkg-overview]。</p>
        </td>
    </tr>
    </tbody>
</table>



## 从父命令继承的选项 {#options_inherited_from_parent_commands}
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
        <td colspan="2">--cache-dir string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;默认值: &#34;$HOME/.kube/cache&#34;</td>
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
        <td colspan="2">--insecure-skip-tls-verify</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，则不会检查服务器证书的有效性。这将使您的 HTTPS 连接不安全</p>
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
            <p>如果存在，则为此 CLI 请求的命名空间范围</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--request-timeout string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;默认值: &#34;0&#34;</td>
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



## 另请参阅 {#see_also}

* [kueuectl list](../) - 显示资源

