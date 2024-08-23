<!--
The file is auto-generated from the Go source code of the component using the
[generator](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/kjobctl/hack/tools/kjobctl-docs).
-->

# kjobctl create slurm


## Synopsis


Create a slurm job

```
kjobctl create slurm SCRIPT --profile APPLICATION_PROFILE_NAME [--localqueue LOCAL_QUEUE_NAME] [--array ARRAY] [--cpus-per-task QUANTITY] [--gpus-per-task QUANTITY] [--mem-per-task QUANTITY] [--mem-per-cpu QUANTITY] [--mem-per-gpu QUANTITY] [--nodes COUNT] [--ntasks COUNT] [--output FILENAME_PATTERN] [--error FILENAME_PATTERN] [--input FILENAME_PATTERN] [--job-name NAME] [--partition NAME] [--ignore-unknown-flags]
```


## Examples

```
  # Create slurm 
  kjobctl create slurm ./script.sh \ 
	--profile my-application-profile
```


## Options


<table style="width: 100%; table-layout: fixed;">
    <colgroup>
        <col span="1" style="width: 10px;" />
        <col span="1" />
    </colgroup>
    <tbody>
    <tr>
        <td colspan="2">-a, --array string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Submit a job array, multiple jobs to be executed with identical parameters. 
The indexes specification identifies what array index values should be used. 
Multiple values may be specified using a comma separated list and/or a range of values with a &#34;-&#34; separator. For example, &#34;--array=0-15&#34; or &#34;--array=0,6,16-32&#34;. 
A maximum number of simultaneously running tasks from the job array may be specified using a &#34;%&#34; separator. For example &#34;--array=0-15%4&#34; will limit the number of simultaneously running tasks from this job array to 4. 
The minimum index value is 0. The maximum index value is 2147483647.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-c, --cpus-per-task string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>How much cpus a container inside a pod requires.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--dry-run string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;none&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Must be &#34;none&#34;, &#34;server&#34;, or &#34;client&#34;. If client strategy, only print the object that would be sent, without sending it. If server strategy, submit server-side request without persisting the resource.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-e, --error string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Where to redirect std error stream of a task. If not passed it proceeds to stdout, and is available via kubectl logs.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--gpus-per-task string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>How much gpus a container inside a pod requires.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-h, --help</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>help for slurm</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--ignore-unknown-flags</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Ignore all the unsupported flags in the bash script.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--input string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>What to pipe into the script.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--job-name string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>What is the job name.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--localqueue string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Kueue localqueue name which is associated with the resource.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--mem-per-cpu string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>How much memory a container requires, it multiplies the number of requested cpus per task by mem-per-cpu.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--mem-per-gpu string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>How much memory a container requires, it multiplies the number of requested gpus per task by mem-per-gpu.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--mem-per-task string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>How much memory a container requires.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-N, --nodes int</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Number of pods to be used at a time.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--ntasks int&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: 1</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Number of identical containers inside of a pod, usually 1.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-o, --output string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Where to redirect the standard output stream of a task. If not passed it proceeds to stdout, and is available via kubectl logs.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--output-format string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Output format. One of: (json, yaml, name, go-template, go-template-file, template, templatefile, jsonpath, jsonpath-as-json, jsonpath-file).</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--partition string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Local queue name.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-p, --profile string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Application profile contains a template (with defaults set) for running a specific type of application.</p>
        </td>
    </tr>
    </tbody>
</table>



## Options inherited from parent commands
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
            <p>Username to impersonate for the operation. User could be a regular user or a service account in a namespace.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-group strings</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Group to impersonate for the operation, this flag can be repeated to specify multiple groups.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--as-uid string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>UID to impersonate for the operation.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--cache-dir string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;$HOME/.kube/cache&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Default cache directory</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--certificate-authority string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Path to a cert file for the certificate authority</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--client-certificate string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Path to a client certificate file for TLS</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--client-key string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Path to a client key file for TLS</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--cluster string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>The name of the kubeconfig cluster to use</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--context string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>The name of the kubeconfig context to use</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--disable-compression</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>If true, opt-out of response compression for all requests to the server</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--insecure-skip-tls-verify</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>If true, the server&#39;s certificate will not be checked for validity. This will make your HTTPS connections insecure</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--kubeconfig string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Path to the kubeconfig file to use for CLI requests.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-n, --namespace string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>If present, the namespace scope for this CLI request</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--request-timeout string&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;Default: &#34;0&#34;</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>The length of time to wait before giving up on a single server request. Non-zero values should contain a corresponding time unit (e.g. 1s, 2m, 3h). A value of zero means don&#39;t timeout requests.</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">-s, --server string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>The address and port of the Kubernetes API server</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--tls-server-name string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Server name to use for server certificate validation. If it is not provided, the hostname used to contact the server is used</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--token string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>Bearer token for authentication to the API server</p>
        </td>
    </tr>
    <tr>
        <td colspan="2">--user string</td>
    </tr>
    <tr>
        <td></td>
        <td style="line-height: 130%; word-wrap: break-word;">
            <p>The name of the kubeconfig user to use</p>
        </td>
    </tr>
    </tbody>
</table>



## See Also

* [kjobctl_create](_index.md)	 - Create a task

