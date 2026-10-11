package common

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

// BuildGCSStoragePVC builds the operator-managed PersistentVolumeClaim that backs
// the embedded RocksDB GCS store for the given RayCluster. It is only meaningful
// when GCS FT uses the embedded backend and the user has not supplied their own
// claim via Storage.ClaimName. The caller is responsible for setting the
// owner reference.
func BuildGCSStoragePVC(instance *rayv1.RayCluster) *corev1.PersistentVolumeClaim {
	storage := instance.Spec.GcsFaultToleranceOptions.Storage

	accessModes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	if storage != nil && len(storage.AccessModes) > 0 {
		accessModes = storage.AccessModes
	}

	size := resource.MustParse(utils.GCSStorageDefaultSize)
	if storage != nil && storage.Size != nil {
		size = *storage.Size
	}

	var storageClassName *string
	if storage != nil {
		storageClassName = storage.StorageClassName
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.GetGCSStoragePVCName(instance),
			Namespace: instance.Namespace,
			Labels: map[string]string{
				utils.RayClusterLabelKey: instance.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      accessModes,
			StorageClassName: storageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: size,
				},
			},
		},
	}
}
