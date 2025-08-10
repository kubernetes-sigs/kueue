---
title: kueuectl patch
content_type: tool-reference
auto_generated: true
no_list: true
---

## 概要 {#synopsis}


更新资源的字段

## 选项 {#options}


<table style="width: 100%; table-layout: fixed;">
    <colgroup>
        <col span="1" style="width: 10px;" />
        <col span="1" />
    </colgroup>
    <tbody>
    <tr>
        <td colspan="2">-h, --help</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>patch 命令的帮助信息</p>
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
            <p>本次操作中模拟的用户名。该用户可以是普通用户，也可以是某个命名空间中的服务账号。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-group strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>本次操作中模拟的用户组。此参数可重复使用，以指定多个用户组。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-uid string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>本次操作中模拟的用户 UID。</p>
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
            <p>证书颁发机构（CA）的证书文件路径。</p>
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
            <p>如果为 true，则对发送到服务器的所有请求不使用响应压缩。</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--insecure-skip-tls-verify</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>如果为 true，将不会检查服务器证书的有效性。这将使你的 HTTPS 连接不安全</p>
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
            <p>如果设置该参数，则为此 CLI 请求指定命名空间作用域。</p>
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
            <p>用于服务器证书验证的服务器名称。如果未提供，则使用联系服务器所用的主机名</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--token string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>API 服务器身份验证所用的持有者令牌</p>
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

* [kueuectl](../kueuectl/) - 控制 Kueue 队列管理器
* [kueuectl patch clusterqueue](kueuectl_patch_clusterqueue/) - 将"patch clusterqueue"透传给 kubectl
* [kueuectl patch localqueue](kueuectl_patch_localqueue/) - 将"patch localqueue"透传给 kubectl
* [kueuectl patch resourceflavor](kueuectl_patch_resourceflavor/) - 将"patch resourceflavor"透传给 kubectl
* [kueuectl patch workload](kueuectl_patch_workload/) - 将"patch workload"透传给 kubectl

