/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package describe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/util/sets"
	describehelper "k8s.io/kubectl/pkg/describe"
	resourcehelper "k8s.io/kubectl/pkg/util/resource"
	storageutil "k8s.io/kubectl/pkg/util/storage"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// Each level has 2 spaces for describehelper.PrefixWriter
const (
	IndentLevelZero = iota
	IndentLevelOne
	IndentLevelTwo
	IndentLevelThree
)

var maxAnnotationLen = 140

func parseAppProfileModeName(s string) ([]string, error) {
	seg := strings.Split(s, "/")
	if len(seg) != 2 {
		if len(seg) > 2 {
			return nil, errors.New("arguments in mode/name form may not have more than one slash")
		}
		return nil, errors.New("argument must be in mode/name form")
	}

	return seg, nil
}

func resourceFor(mode string) string {
	if strings.EqualFold(mode, string(v1alpha1.InteractiveMode)) {
		return "pod"
	}

	if strings.EqualFold(mode, string(v1alpha1.SlurmMode)) {
		return "job"
	}

	return strings.ToLower(mode)
}

func gvkFor(mapper meta.RESTMapper, resource string) (schema.GroupVersionKind, error) {
	var gvk schema.GroupVersionKind
	var err error

	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(strings.ToLower(resource))
	gvr := schema.GroupVersionResource{}
	if fullySpecifiedGVR != nil {
		gvr, _ = mapper.ResourceFor(*fullySpecifiedGVR)
	}
	if gvr.Empty() {
		gvr, err = mapper.ResourceFor(groupResource.WithVersion(""))
		if err != nil {
			return gvk, err
		}
	}

	gvk, err = mapper.KindFor(gvr)
	if err != nil {
		return gvk, err
	}

	return gvk, err
}

// below functions copied from https://github.com/kubernetes/kubectl/blob/master/pkg/describe/describe.go

func tabbedString(f func(io.Writer) error) (string, error) {
	out := new(tabwriter.Writer)
	buf := &bytes.Buffer{}
	out.Init(buf, 0, 8, 2, ' ', 0)

	err := f(out)
	if err != nil {
		return "", err
	}

	out.Flush()
	return buf.String(), nil
}

// printLabelsMultiline prints multiple labels with a proper alignment.
func printLabelsMultiline(w describehelper.PrefixWriter, title string, labels map[string]string) {
	printLabelsMultilineWithIndent(w, "", title, "\t", labels, sets.New[string]())
}

// printLabelsMultiline prints multiple labels with a user-defined alignment.
func printLabelsMultilineWithIndent(w describehelper.PrefixWriter, initialIndent, title, innerIndent string, labels map[string]string, skip sets.Set[string]) {
	w.Write(IndentLevelZero, "%s%s:%s", initialIndent, title, innerIndent)

	if len(labels) == 0 {
		w.WriteLine("<none>")
		return
	}

	// to print labels in the sorted order
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if skip.Has(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		w.WriteLine("<none>")
		return
	}
	sort.Strings(keys)

	for i, key := range keys {
		if i != 0 {
			w.Write(IndentLevelZero, "%s", initialIndent)
			w.Write(IndentLevelZero, "%s", innerIndent)
		}
		w.Write(IndentLevelZero, "%s=%s\n", key, labels[key])
	}
}

func describePodTemplate(template *corev1.PodTemplateSpec, w describehelper.PrefixWriter) {
	w.Write(IndentLevelZero, "Pod Template:\n")
	if template == nil {
		w.Write(IndentLevelOne, "<unset>")
		return
	}

	if len(template.Spec.InitContainers) > 0 {
		describeContainers("Init Containers", template.Spec.InitContainers, w, "  ")
	}
	describeContainers("Containers", template.Spec.Containers, w, "  ")
	describeVolumes(template.Spec.Volumes, w, "  ")
}

func describeContainers(label string, containers []corev1.Container, w describehelper.PrefixWriter, space string) {
	describeContainersLabel(containers, label, space, w)

	for _, container := range containers {
		describeContainerBasicInfo(container, space, w)
		describeContainerCommand(container, w)
		describeContainerResource(container, w)
		describeContainerProbe(container, w)
		if len(container.EnvFrom) > 0 {
			describeContainerEnvFrom(container, w)
		}
		describeContainerEnvVars(container, w)
		describeContainerVolumes(container, w)
	}
}

func describeContainersLabel(containers []corev1.Container, label, space string, w describehelper.PrefixWriter) {
	none := ""
	if len(containers) == 0 {
		none = " <none>"
	}
	w.Write(IndentLevelZero, "%s%s:%s\n", space, label, none)
}

func describeContainerBasicInfo(container corev1.Container, space string, w describehelper.PrefixWriter) {
	nameIndent := ""
	if len(space) > 0 {
		nameIndent = " "
	}
	w.Write(IndentLevelOne, "%s%v:\n", nameIndent, container.Name)
	portString := describeContainerPorts(container.Ports)
	if strings.Contains(portString, ",") {
		w.Write(IndentLevelTwo, "Ports:\t%s\n", portString)
	} else {
		w.Write(IndentLevelTwo, "Port:\t%s\n", stringOrNone(portString))
	}
	hostPortString := describeContainerHostPorts(container.Ports)
	if strings.Contains(hostPortString, ",") {
		w.Write(IndentLevelTwo, "Host Ports:\t%s\n", hostPortString)
	} else {
		w.Write(IndentLevelTwo, "Host Port:\t%s\n", stringOrNone(hostPortString))
	}
}

func stringOrNone(s string) string {
	return stringOrDefaultValue(s, "<none>")
}

func stringOrDefaultValue(s, defaultValue string) string {
	if len(s) > 0 {
		return s
	}
	return defaultValue
}

func describeContainerPorts(cPorts []corev1.ContainerPort) string {
	ports := make([]string, 0, len(cPorts))
	for _, cPort := range cPorts {
		ports = append(ports, fmt.Sprintf("%d/%s", cPort.ContainerPort, cPort.Protocol))
	}
	return strings.Join(ports, ", ")
}

func describeContainerHostPorts(cPorts []corev1.ContainerPort) string {
	ports := make([]string, 0, len(cPorts))
	for _, cPort := range cPorts {
		ports = append(ports, fmt.Sprintf("%d/%s", cPort.HostPort, cPort.Protocol))
	}
	return strings.Join(ports, ", ")
}

func describeContainerCommand(container corev1.Container, w describehelper.PrefixWriter) {
	if len(container.Command) > 0 {
		w.Write(IndentLevelTwo, "Command:\n")
		for _, c := range container.Command {
			for _, s := range strings.Split(c, "\n") {
				w.Write(IndentLevelThree, "%s\n", s)
			}
		}
	}
	if len(container.Args) > 0 {
		w.Write(IndentLevelTwo, "Args:\n")
		for _, arg := range container.Args {
			for _, s := range strings.Split(arg, "\n") {
				w.Write(IndentLevelThree, "%s\n", s)
			}
		}
	}
}

func describeContainerResource(container corev1.Container, w describehelper.PrefixWriter) {
	resources := container.Resources
	if len(resources.Limits) > 0 {
		w.Write(IndentLevelTwo, "Limits:\n")
	}
	for _, name := range describehelper.SortedResourceNames(resources.Limits) {
		quantity := resources.Limits[name]
		w.Write(IndentLevelThree, "%s:\t%s\n", name, quantity.String())
	}

	if len(resources.Requests) > 0 {
		w.Write(IndentLevelTwo, "Requests:\n")
	}
	for _, name := range describehelper.SortedResourceNames(resources.Requests) {
		quantity := resources.Requests[name]
		w.Write(IndentLevelThree, "%s:\t%s\n", name, quantity.String())
	}
}

func describeContainerProbe(container corev1.Container, w describehelper.PrefixWriter) {
	if container.LivenessProbe != nil {
		probe := describehelper.DescribeProbe(container.LivenessProbe)
		w.Write(IndentLevelTwo, "Liveness:\t%s\n", probe)
	}
	if container.ReadinessProbe != nil {
		probe := describehelper.DescribeProbe(container.ReadinessProbe)
		w.Write(IndentLevelTwo, "Readiness:\t%s\n", probe)
	}
	if container.StartupProbe != nil {
		probe := describehelper.DescribeProbe(container.StartupProbe)
		w.Write(IndentLevelTwo, "Startup:\t%s\n", probe)
	}
}

func describeContainerVolumes(container corev1.Container, w describehelper.PrefixWriter) {
	// Show volumeMounts
	none := ""
	if len(container.VolumeMounts) == 0 {
		none = "\t<none>"
	}
	w.Write(IndentLevelTwo, "Mounts:%s\n", none)
	sort.Sort(describehelper.SortableVolumeMounts(container.VolumeMounts))
	for _, mount := range container.VolumeMounts {
		flags := []string{}
		if mount.ReadOnly {
			flags = append(flags, "ro")
		} else {
			flags = append(flags, "rw")
		}
		if len(mount.SubPath) > 0 {
			flags = append(flags, fmt.Sprintf("path=%q", mount.SubPath))
		}
		w.Write(IndentLevelThree, "%s from %s (%s)\n", mount.MountPath, mount.Name, strings.Join(flags, ","))
	}
	// Show volumeDevices if exists
	if len(container.VolumeDevices) > 0 {
		w.Write(IndentLevelTwo, "Devices:%s\n", none)
		sort.Sort(describehelper.SortableVolumeDevices(container.VolumeDevices))
		for _, device := range container.VolumeDevices {
			w.Write(IndentLevelThree, "%s from %s\n", device.DevicePath, device.Name)
		}
	}
}

func describeContainerEnvVars(container corev1.Container, w describehelper.PrefixWriter) {
	none := ""
	if len(container.Env) == 0 {
		none = "\t<none>"
	}
	w.Write(IndentLevelTwo, "Environment:%s\n", none)

	for _, e := range container.Env {
		if e.ValueFrom == nil {
			for i, s := range strings.Split(e.Value, "\n") {
				if i == 0 {
					w.Write(IndentLevelThree, "%s:\t%s\n", e.Name, s)
				} else {
					w.Write(IndentLevelThree, "\t%s\n", s)
				}
			}
			continue
		}

		switch {
		case e.ValueFrom.FieldRef != nil:
			w.Write(IndentLevelThree, "%s:\t%s (%s:%s)\n", e.Name, "", e.ValueFrom.FieldRef.APIVersion, e.ValueFrom.FieldRef.FieldPath)
		case e.ValueFrom.ResourceFieldRef != nil:
			valueFrom, err := resourcehelper.ExtractContainerResourceValue(e.ValueFrom.ResourceFieldRef, &container)
			if err != nil {
				valueFrom = ""
			}
			resource := e.ValueFrom.ResourceFieldRef.Resource
			if valueFrom == "0" && (resource == "limits.cpu" || resource == "limits.memory") {
				valueFrom = "node allocatable"
			}
			w.Write(IndentLevelThree, "%s:\t%s (%s)\n", e.Name, valueFrom, resource)
		case e.ValueFrom.SecretKeyRef != nil:
			optional := e.ValueFrom.SecretKeyRef.Optional != nil && *e.ValueFrom.SecretKeyRef.Optional
			w.Write(IndentLevelThree, "%s:\t<set to the key '%s' in secret '%s'>\tOptional: %t\n", e.Name, e.ValueFrom.SecretKeyRef.Key, e.ValueFrom.SecretKeyRef.Name, optional)
		case e.ValueFrom.ConfigMapKeyRef != nil:
			optional := e.ValueFrom.ConfigMapKeyRef.Optional != nil && *e.ValueFrom.ConfigMapKeyRef.Optional
			w.Write(IndentLevelThree, "%s:\t<set to the key '%s' of config map '%s'>\tOptional: %t\n", e.Name, e.ValueFrom.ConfigMapKeyRef.Key, e.ValueFrom.ConfigMapKeyRef.Name, optional)
		}
	}
}

func describeContainerEnvFrom(container corev1.Container, w describehelper.PrefixWriter) {
	none := ""
	if len(container.EnvFrom) == 0 {
		none = "\t<none>"
	}
	w.Write(IndentLevelTwo, "Environment Variables from:%s\n", none)

	for _, e := range container.EnvFrom {
		from := ""
		name := ""
		optional := false
		if e.ConfigMapRef != nil {
			from = "ConfigMap"
			name = e.ConfigMapRef.Name
			optional = e.ConfigMapRef.Optional != nil && *e.ConfigMapRef.Optional
		} else if e.SecretRef != nil {
			from = "Secret"
			name = e.SecretRef.Name
			optional = e.SecretRef.Optional != nil && *e.SecretRef.Optional
		}
		if len(e.Prefix) == 0 {
			w.Write(IndentLevelThree, "%s\t%s\tOptional: %t\n", name, from, optional)
		} else {
			w.Write(IndentLevelThree, "%s\t%s with prefix '%s'\tOptional: %t\n", name, from, e.Prefix, optional)
		}
	}
}

func describeVolumes(volumes []corev1.Volume, w describehelper.PrefixWriter, space string) {
	if len(volumes) == 0 {
		w.Write(IndentLevelZero, "%sVolumes:\t<none>\n", space)
		return
	}

	w.Write(IndentLevelZero, "%sVolumes:\n", space)
	for _, volume := range volumes {
		nameIndent := ""
		if len(space) > 0 {
			nameIndent = " "
		}
		w.Write(IndentLevelOne, "%s%v:\n", nameIndent, volume.Name)
		switch {
		case volume.VolumeSource.HostPath != nil:
			printHostPathVolumeSource(volume.VolumeSource.HostPath, w)
		case volume.VolumeSource.EmptyDir != nil:
			printEmptyDirVolumeSource(volume.VolumeSource.EmptyDir, w)
		case volume.VolumeSource.GCEPersistentDisk != nil:
			printGCEPersistentDiskVolumeSource(volume.VolumeSource.GCEPersistentDisk, w)
		case volume.VolumeSource.AWSElasticBlockStore != nil:
			printAWSElasticBlockStoreVolumeSource(volume.VolumeSource.AWSElasticBlockStore, w)
		case volume.VolumeSource.GitRepo != nil:
			printGitRepoVolumeSource(volume.VolumeSource.GitRepo, w)
		case volume.VolumeSource.Secret != nil:
			printSecretVolumeSource(volume.VolumeSource.Secret, w)
		case volume.VolumeSource.ConfigMap != nil:
			printConfigMapVolumeSource(volume.VolumeSource.ConfigMap, w)
		case volume.VolumeSource.NFS != nil:
			printNFSVolumeSource(volume.VolumeSource.NFS, w)
		case volume.VolumeSource.ISCSI != nil:
			printISCSIVolumeSource(volume.VolumeSource.ISCSI, w)
		case volume.VolumeSource.Glusterfs != nil:
			printGlusterfsVolumeSource(volume.VolumeSource.Glusterfs, w)
		case volume.VolumeSource.PersistentVolumeClaim != nil:
			printPersistentVolumeClaimVolumeSource(volume.VolumeSource.PersistentVolumeClaim, w)
		case volume.VolumeSource.Ephemeral != nil:
			printEphemeralVolumeSource(volume.VolumeSource.Ephemeral, w)
		case volume.VolumeSource.RBD != nil:
			printRBDVolumeSource(volume.VolumeSource.RBD, w)
		case volume.VolumeSource.Quobyte != nil:
			printQuobyteVolumeSource(volume.VolumeSource.Quobyte, w)
		case volume.VolumeSource.DownwardAPI != nil:
			printDownwardAPIVolumeSource(volume.VolumeSource.DownwardAPI, w)
		case volume.VolumeSource.AzureDisk != nil:
			printAzureDiskVolumeSource(volume.VolumeSource.AzureDisk, w)
		case volume.VolumeSource.VsphereVolume != nil:
			printVsphereVolumeSource(volume.VolumeSource.VsphereVolume, w)
		case volume.VolumeSource.Cinder != nil:
			printCinderVolumeSource(volume.VolumeSource.Cinder, w)
		case volume.VolumeSource.PhotonPersistentDisk != nil:
			printPhotonPersistentDiskVolumeSource(volume.VolumeSource.PhotonPersistentDisk, w)
		case volume.VolumeSource.PortworxVolume != nil:
			printPortworxVolumeSource(volume.VolumeSource.PortworxVolume, w)
		case volume.VolumeSource.ScaleIO != nil:
			printScaleIOVolumeSource(volume.VolumeSource.ScaleIO, w)
		case volume.VolumeSource.CephFS != nil:
			printCephFSVolumeSource(volume.VolumeSource.CephFS, w)
		case volume.VolumeSource.StorageOS != nil:
			printStorageOSVolumeSource(volume.VolumeSource.StorageOS, w)
		case volume.VolumeSource.FC != nil:
			printFCVolumeSource(volume.VolumeSource.FC, w)
		case volume.VolumeSource.AzureFile != nil:
			printAzureFileVolumeSource(volume.VolumeSource.AzureFile, w)
		case volume.VolumeSource.FlexVolume != nil:
			printFlexVolumeSource(volume.VolumeSource.FlexVolume, w)
		case volume.VolumeSource.Flocker != nil:
			printFlockerVolumeSource(volume.VolumeSource.Flocker, w)
		case volume.VolumeSource.Projected != nil:
			printProjectedVolumeSource(volume.VolumeSource.Projected, w)
		case volume.VolumeSource.CSI != nil:
			printCSIVolumeSource(volume.VolumeSource.CSI, w)
		default:
			w.Write(IndentLevelOne, "<unknown>\n")
		}
	}
}

func printHostPathVolumeSource(hostPath *corev1.HostPathVolumeSource, w describehelper.PrefixWriter) {
	hostPathType := "<none>"
	if hostPath.Type != nil {
		hostPathType = string(*hostPath.Type)
	}
	w.Write(IndentLevelTwo, "Type:\tHostPath (bare host directory volume)\n"+
		"    Path:\t%v\n"+
		"    HostPathType:\t%v\n",
		hostPath.Path, hostPathType)
}

func printEmptyDirVolumeSource(emptyDir *corev1.EmptyDirVolumeSource, w describehelper.PrefixWriter) {
	var sizeLimit string
	if emptyDir.SizeLimit != nil && emptyDir.SizeLimit.Cmp(resource.Quantity{}) > 0 {
		sizeLimit = fmt.Sprintf("%v", emptyDir.SizeLimit)
	} else {
		sizeLimit = "<unset>"
	}
	w.Write(IndentLevelTwo, "Type:\tEmptyDir (a temporary directory that shares a pod's lifetime)\n"+
		"    Medium:\t%v\n"+
		"    SizeLimit:\t%v\n",
		emptyDir.Medium, sizeLimit)
}

func printGCEPersistentDiskVolumeSource(gce *corev1.GCEPersistentDiskVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tGCEPersistentDisk (a Persistent Disk resource in Google Compute Engine)\n"+
		"    PDName:\t%v\n"+
		"    FSType:\t%v\n"+
		"    Partition:\t%v\n"+
		"    ReadOnly:\t%v\n",
		gce.PDName, gce.FSType, gce.Partition, gce.ReadOnly)
}

func printAWSElasticBlockStoreVolumeSource(aws *corev1.AWSElasticBlockStoreVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tAWSElasticBlockStore (a Persistent Disk resource in AWS)\n"+
		"    VolumeID:\t%v\n"+
		"    FSType:\t%v\n"+
		"    Partition:\t%v\n"+
		"    ReadOnly:\t%v\n",
		aws.VolumeID, aws.FSType, aws.Partition, aws.ReadOnly)
}

func printGitRepoVolumeSource(git *corev1.GitRepoVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tGitRepo (a volume that is pulled from git when the pod is created)\n"+
		"    Repository:\t%v\n"+
		"    Revision:\t%v\n",
		git.Repository, git.Revision)
}

func printSecretVolumeSource(secret *corev1.SecretVolumeSource, w describehelper.PrefixWriter) {
	optional := secret.Optional != nil && *secret.Optional
	w.Write(IndentLevelTwo, "Type:\tSecret (a volume populated by a Secret)\n"+
		"    SecretName:\t%v\n"+
		"    Optional:\t%v\n",
		secret.SecretName, optional)
}

func printConfigMapVolumeSource(configMap *corev1.ConfigMapVolumeSource, w describehelper.PrefixWriter) {
	optional := configMap.Optional != nil && *configMap.Optional
	w.Write(IndentLevelTwo, "Type:\tConfigMap (a volume populated by a ConfigMap)\n"+
		"    Name:\t%v\n"+
		"    Optional:\t%v\n",
		configMap.Name, optional)
}

func printProjectedVolumeSource(projected *corev1.ProjectedVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tProjected (a volume that contains injected data from multiple sources)\n")
	for _, source := range projected.Sources {
		if source.Secret != nil {
			w.Write(IndentLevelTwo, "SecretName:\t%v\n"+
				"    SecretOptionalName:\t%v\n",
				source.Secret.Name, source.Secret.Optional)
			continue
		}
		if source.DownwardAPI != nil {
			w.Write(IndentLevelTwo, "DownwardAPI:\ttrue\n")
			continue
		}
		if source.ConfigMap != nil {
			w.Write(IndentLevelTwo, "ConfigMapName:\t%v\n"+
				"    ConfigMapOptional:\t%v\n",
				source.ConfigMap.Name, source.ConfigMap.Optional)
			continue
		}
		if source.ServiceAccountToken != nil {
			w.Write(IndentLevelTwo, "TokenExpirationSeconds:\t%d\n",
				*source.ServiceAccountToken.ExpirationSeconds)
		}
	}
}

func printNFSVolumeSource(nfs *corev1.NFSVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tNFS (an NFS mount that lasts the lifetime of a pod)\n"+
		"    Server:\t%v\n"+
		"    Path:\t%v\n"+
		"    ReadOnly:\t%v\n",
		nfs.Server, nfs.Path, nfs.ReadOnly)
}

func printQuobyteVolumeSource(quobyte *corev1.QuobyteVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tQuobyte (a Quobyte mount on the host that shares a pod's lifetime)\n"+
		"    Registry:\t%v\n"+
		"    Volume:\t%v\n"+
		"    ReadOnly:\t%v\n",
		quobyte.Registry, quobyte.Volume, quobyte.ReadOnly)
}

func printPortworxVolumeSource(pwxVolume *corev1.PortworxVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tPortworxVolume (a Portworx Volume resource)\n"+
		"    VolumeID:\t%v\n",
		pwxVolume.VolumeID)
}

func printISCSIVolumeSource(iscsi *corev1.ISCSIVolumeSource, w describehelper.PrefixWriter) {
	initiator := "<none>"
	if iscsi.InitiatorName != nil {
		initiator = *iscsi.InitiatorName
	}
	w.Write(IndentLevelTwo, "Type:\tISCSI (an ISCSI Disk resource that is attached to a kubelet's host machine and then exposed to the pod)\n"+
		"    TargetPortal:\t%v\n"+
		"    IQN:\t%v\n"+
		"    Lun:\t%v\n"+
		"    ISCSIInterface\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n"+
		"    Portals:\t%v\n"+
		"    DiscoveryCHAPAuth:\t%v\n"+
		"    SessionCHAPAuth:\t%v\n"+
		"    SecretRef:\t%v\n"+
		"    InitiatorName:\t%v\n",
		iscsi.TargetPortal, iscsi.IQN, iscsi.Lun, iscsi.ISCSIInterface, iscsi.FSType, iscsi.ReadOnly, iscsi.Portals, iscsi.DiscoveryCHAPAuth, iscsi.SessionCHAPAuth, iscsi.SecretRef, initiator)
}

func printGlusterfsVolumeSource(glusterfs *corev1.GlusterfsVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tGlusterfs (a Glusterfs mount on the host that shares a pod's lifetime)\n"+
		"    EndpointsName:\t%v\n"+
		"    Path:\t%v\n"+
		"    ReadOnly:\t%v\n",
		glusterfs.EndpointsName, glusterfs.Path, glusterfs.ReadOnly)
}

func printPersistentVolumeClaimVolumeSource(claim *corev1.PersistentVolumeClaimVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tPersistentVolumeClaim (a reference to a PersistentVolumeClaim in the same namespace)\n"+
		"    ClaimName:\t%v\n"+
		"    ReadOnly:\t%v\n",
		claim.ClaimName, claim.ReadOnly)
}

func printEphemeralVolumeSource(ephemeral *corev1.EphemeralVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tEphemeralVolume (an inline specification for a volume that gets created and deleted with the pod)\n")
	if ephemeral.VolumeClaimTemplate != nil {
		printPersistentVolumeClaim(describehelper.NewNestedPrefixWriter(w, IndentLevelTwo),
			&corev1.PersistentVolumeClaim{
				ObjectMeta: ephemeral.VolumeClaimTemplate.ObjectMeta,
				Spec:       ephemeral.VolumeClaimTemplate.Spec,
			}, false /* not a full PVC */)
	}
}

// printPersistentVolumeClaim is used for both PVCs and PersistentVolumeClaimTemplate. For the latter,
// we need to skip some fields which have no meaning.
func printPersistentVolumeClaim(w describehelper.PrefixWriter, pvc *corev1.PersistentVolumeClaim, isFullPVC bool) {
	if isFullPVC {
		w.Write(IndentLevelZero, "Name:\t%s\n", pvc.Name)
		w.Write(IndentLevelZero, "Namespace:\t%s\n", pvc.Namespace)
	}
	w.Write(IndentLevelZero, "StorageClass:\t%s\n", storageutil.GetPersistentVolumeClaimClass(pvc))
	if isFullPVC {
		if pvc.ObjectMeta.DeletionTimestamp != nil {
			w.Write(IndentLevelZero, "Status:\tTerminating (lasts %s)\n", translateTimestampSince(*pvc.ObjectMeta.DeletionTimestamp))
		} else {
			w.Write(IndentLevelZero, "Status:\t%v\n", pvc.Status.Phase)
		}
	}
	w.Write(IndentLevelZero, "Volume:\t%s\n", pvc.Spec.VolumeName)
	printLabelsMultiline(w, "Labels", pvc.Labels)
	if isFullPVC {
		w.Write(IndentLevelZero, "Finalizers:\t%v\n", pvc.ObjectMeta.Finalizers)
	}
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	capacity := ""
	accessModes := ""
	if pvc.Spec.VolumeName != "" {
		accessModes = storageutil.GetAccessModesAsString(pvc.Status.AccessModes)
		storage = pvc.Status.Capacity[corev1.ResourceStorage]
		capacity = storage.String()
	}
	w.Write(IndentLevelZero, "Capacity:\t%s\n", capacity)
	w.Write(IndentLevelZero, "Access Modes:\t%s\n", accessModes)
	if pvc.Spec.VolumeMode != nil {
		w.Write(IndentLevelZero, "VolumeMode:\t%v\n", *pvc.Spec.VolumeMode)
	}
	if pvc.Spec.DataSource != nil {
		w.Write(IndentLevelZero, "DataSource:\n")
		if pvc.Spec.DataSource.APIGroup != nil {
			w.Write(IndentLevelOne, "APIGroup:\t%v\n", *pvc.Spec.DataSource.APIGroup)
		}
		w.Write(IndentLevelOne, "Kind:\t%v\n", pvc.Spec.DataSource.Kind)
		w.Write(IndentLevelOne, "Name:\t%v\n", pvc.Spec.DataSource.Name)
	}
}

// translateTimestampSince returns the elapsed time since timestamp in
// human-readable approximation.
func translateTimestampSince(timestamp metav1.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}

	return duration.HumanDuration(time.Since(timestamp.Time))
}

func printRBDVolumeSource(rbd *corev1.RBDVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tRBD (a Rados Block Device mount on the host that shares a pod's lifetime)\n"+
		"    CephMonitors:\t%v\n"+
		"    RBDImage:\t%v\n"+
		"    FSType:\t%v\n"+
		"    RBDPool:\t%v\n"+
		"    RadosUser:\t%v\n"+
		"    Keyring:\t%v\n"+
		"    SecretRef:\t%v\n"+
		"    ReadOnly:\t%v\n",
		rbd.CephMonitors, rbd.RBDImage, rbd.FSType, rbd.RBDPool, rbd.RadosUser, rbd.Keyring, rbd.SecretRef, rbd.ReadOnly)
}

func printDownwardAPIVolumeSource(d *corev1.DownwardAPIVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tDownwardAPI (a volume populated by information about the pod)\n    Items:\n")
	for _, mapping := range d.Items {
		if mapping.FieldRef != nil {
			w.Write(IndentLevelThree, "%v -> %v\n", mapping.FieldRef.FieldPath, mapping.Path)
		}
		if mapping.ResourceFieldRef != nil {
			w.Write(IndentLevelThree, "%v -> %v\n", mapping.ResourceFieldRef.Resource, mapping.Path)
		}
	}
}

func printAzureDiskVolumeSource(d *corev1.AzureDiskVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tAzureDisk (an Azure Data Disk mount on the host and bind mount to the pod)\n"+
		"    DiskName:\t%v\n"+
		"    DiskURI:\t%v\n"+
		"    Kind: \t%v\n"+
		"    FSType:\t%v\n"+
		"    CachingMode:\t%v\n"+
		"    ReadOnly:\t%v\n",
		d.DiskName, d.DataDiskURI, *d.Kind, *d.FSType, *d.CachingMode, *d.ReadOnly)
}

func printVsphereVolumeSource(vsphere *corev1.VsphereVirtualDiskVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tvSphereVolume (a Persistent Disk resource in vSphere)\n"+
		"    VolumePath:\t%v\n"+
		"    FSType:\t%v\n"+
		"    StoragePolicyName:\t%v\n",
		vsphere.VolumePath, vsphere.FSType, vsphere.StoragePolicyName)
}

func printPhotonPersistentDiskVolumeSource(photon *corev1.PhotonPersistentDiskVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tPhotonPersistentDisk (a Persistent Disk resource in photon platform)\n"+
		"    PdID:\t%v\n"+
		"    FSType:\t%v\n",
		photon.PdID, photon.FSType)
}

func printCinderVolumeSource(cinder *corev1.CinderVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tCinder (a Persistent Disk resource in OpenStack)\n"+
		"    VolumeID:\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n"+
		"    SecretRef:\t%v\n",
		cinder.VolumeID, cinder.FSType, cinder.ReadOnly, cinder.SecretRef)
}

func printScaleIOVolumeSource(sio *corev1.ScaleIOVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tScaleIO (a persistent volume backed by a block device in ScaleIO)\n"+
		"    Gateway:\t%v\n"+
		"    System:\t%v\n"+
		"    Protection Domain:\t%v\n"+
		"    Storage Pool:\t%v\n"+
		"    Storage Mode:\t%v\n"+
		"    VolumeName:\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n",
		sio.Gateway, sio.System, sio.ProtectionDomain, sio.StoragePool, sio.StorageMode, sio.VolumeName, sio.FSType, sio.ReadOnly)
}

func printCephFSVolumeSource(cephfs *corev1.CephFSVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tCephFS (a CephFS mount on the host that shares a pod's lifetime)\n"+
		"    Monitors:\t%v\n"+
		"    Path:\t%v\n"+
		"    User:\t%v\n"+
		"    SecretFile:\t%v\n"+
		"    SecretRef:\t%v\n"+
		"    ReadOnly:\t%v\n",
		cephfs.Monitors, cephfs.Path, cephfs.User, cephfs.SecretFile, cephfs.SecretRef, cephfs.ReadOnly)
}

func printStorageOSVolumeSource(storageos *corev1.StorageOSVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tStorageOS (a StorageOS Persistent Disk resource)\n"+
		"    VolumeName:\t%v\n"+
		"    VolumeNamespace:\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n",
		storageos.VolumeName, storageos.VolumeNamespace, storageos.FSType, storageos.ReadOnly)
}

func printFCVolumeSource(fc *corev1.FCVolumeSource, w describehelper.PrefixWriter) {
	lun := "<none>"
	if fc.Lun != nil {
		lun = strconv.Itoa(int(*fc.Lun))
	}
	w.Write(IndentLevelTwo, "Type:\tFC (a Fibre Channel disk)\n"+
		"    TargetWWNs:\t%v\n"+
		"    LUN:\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n",
		strings.Join(fc.TargetWWNs, ", "), lun, fc.FSType, fc.ReadOnly)
}

func printAzureFileVolumeSource(azureFile *corev1.AzureFileVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tAzureFile (an Azure File Service mount on the host and bind mount to the pod)\n"+
		"    SecretName:\t%v\n"+
		"    ShareName:\t%v\n"+
		"    ReadOnly:\t%v\n",
		azureFile.SecretName, azureFile.ShareName, azureFile.ReadOnly)
}

func printFlexVolumeSource(flex *corev1.FlexVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tFlexVolume (a generic volume resource that is provisioned/attached using an exec based plugin)\n"+
		"    Driver:\t%v\n"+
		"    FSType:\t%v\n"+
		"    SecretRef:\t%v\n"+
		"    ReadOnly:\t%v\n"+
		"    Options:\t%v\n",
		flex.Driver, flex.FSType, flex.SecretRef, flex.ReadOnly, flex.Options)
}

func printFlockerVolumeSource(flocker *corev1.FlockerVolumeSource, w describehelper.PrefixWriter) {
	w.Write(IndentLevelTwo, "Type:\tFlocker (a Flocker volume mounted by the Flocker agent)\n"+
		"    DatasetName:\t%v\n"+
		"    DatasetUUID:\t%v\n",
		flocker.DatasetName, flocker.DatasetUUID)
}

func printCSIVolumeSource(csi *corev1.CSIVolumeSource, w describehelper.PrefixWriter) {
	var readOnly bool
	var fsType string
	if csi.ReadOnly != nil && *csi.ReadOnly {
		readOnly = true
	}
	if csi.FSType != nil {
		fsType = *csi.FSType
	}
	w.Write(IndentLevelTwo, "Type:\tCSI (a Container Storage Interface (CSI) volume source)\n"+
		"    Driver:\t%v\n"+
		"    FSType:\t%v\n"+
		"    ReadOnly:\t%v\n",
		csi.Driver, fsType, readOnly)
	printCSIPersistentVolumeAttributesMultiline(w, "VolumeAttributes", csi.VolumeAttributes)
}

func printCSIPersistentVolumeAttributesMultiline(w describehelper.PrefixWriter, title string, annotations map[string]string) {
	printCSIPersistentVolumeAttributesMultilineIndent(w, "", title, "\t", annotations, sets.New[string]())
}

func printCSIPersistentVolumeAttributesMultilineIndent(w describehelper.PrefixWriter, initialIndent, title, innerIndent string, attributes map[string]string, skip sets.Set[string]) {
	w.Write(IndentLevelTwo, "%s%s:%s", initialIndent, title, innerIndent)

	if len(attributes) == 0 {
		w.WriteLine("<none>")
		return
	}

	// to print labels in the sorted order
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		if skip.Has(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(attributes) == 0 {
		w.WriteLine("<none>")
		return
	}
	sort.Strings(keys)

	for i, key := range keys {
		if i != 0 {
			w.Write(IndentLevelTwo, initialIndent)
			w.Write(IndentLevelTwo, innerIndent)
		}
		line := fmt.Sprintf("%s=%s", key, attributes[key])
		if len(line) > maxAnnotationLen {
			w.Write(IndentLevelTwo, "%s...\n", line[:maxAnnotationLen])
		} else {
			w.Write(IndentLevelTwo, "%s\n", line)
		}
	}
}
