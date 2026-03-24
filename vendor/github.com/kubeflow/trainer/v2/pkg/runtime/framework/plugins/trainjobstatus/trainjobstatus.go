/*
Copyright 2026 The Kubeflow Authors.

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

package trainjobstatus

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	"github.com/kubeflow/trainer/v2/pkg/statusserver"
	"github.com/kubeflow/trainer/v2/pkg/util/cert"
)

const (
	Name = "TrainJobStatus"

	// Environment variable names
	envNameStatusURL = "KUBEFLOW_TRAINER_SERVER_URL"
	envNameCACert    = "KUBEFLOW_TRAINER_SERVER_CA_CERT"
	envNameToken     = "KUBEFLOW_TRAINER_SERVER_TOKEN"

	// Volume and mount configuration
	configMountPath = "/var/run/secrets/kubeflow/trainer"
	caCertFileName  = "ca.crt"
	tokenFileName   = "token"
	tokenVolumeName = "kubeflow-trainer-token"

	// Service account token configuration
	tokenExpirySeconds = 3600

	// Server tls config
	caCertKey = "ca.crt"
)

type Status struct {
	client client.Client
	cfg    *configapi.Configuration
}

var _ framework.ComponentBuilderPlugin = (*Status)(nil)
var _ framework.EnforceMLPolicyPlugin = (*Status)(nil)

func New(_ context.Context, c client.Client, _ client.FieldIndexer, cfg *configapi.Configuration) (framework.Plugin, error) {
	return &Status{client: c, cfg: cfg}, nil
}

func (p *Status) Name() string {
	return Name
}

func (p *Status) EnforceMLPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || trainJob == nil {
		return nil
	}

	envVars, err := p.createEnvVars(trainJob)
	if err != nil {
		return err
	}
	volumeMount := createTokenVolumeMount()
	volume := createTokenVolume(trainJob)

	// Inject into all trainer containers
	trainerPS := info.FindPodSetByAncestor(constants.AncestorTrainer)
	if trainerPS != nil {
		for i := range trainerPS.Containers {
			apply.UpsertEnvVars(&trainerPS.Containers[i].Env, envVars...)
			apply.UpsertVolumeMounts(&trainerPS.Containers[i].VolumeMounts, volumeMount)
		}
		apply.UpsertVolumes(&trainerPS.Volumes, volume)
	}

	return nil
}

func (p *Status) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	if info == nil || trainJob == nil {
		return nil, nil
	}

	configMap, err := p.buildStatusServerCaCrtConfigMap(ctx, trainJob)
	if err != nil {
		return nil, err
	}

	return []apiruntime.ApplyConfiguration{configMap}, nil
}

func (p *Status) createEnvVars(trainJob *trainer.TrainJob) ([]corev1ac.EnvVarApplyConfiguration, error) {
	if p.cfg.StatusServer.Port == nil {
		return nil, fmt.Errorf("missing status server port")
	}
	// TODO: consider renaming the CertManagement.WebhookServiceName name?
	svc := fmt.Sprintf("https://%s.%s.svc:%d", p.cfg.CertManagement.WebhookServiceName, cert.GetOperatorNamespace(), *p.cfg.StatusServer.Port)
	path := statusserver.StatusUrl(trainJob.Namespace, trainJob.Name)
	statusURL := svc + path

	return []corev1ac.EnvVarApplyConfiguration{
		*corev1ac.EnvVar().
			WithName(envNameStatusURL).
			WithValue(statusURL),
		*corev1ac.EnvVar().
			WithName(envNameCACert).
			WithValue(fmt.Sprintf("%s/%s", configMountPath, caCertFileName)),
		*corev1ac.EnvVar().
			WithName(envNameToken).
			WithValue(fmt.Sprintf("%s/%s", configMountPath, tokenFileName)),
	}, nil
}

func createTokenVolumeMount() corev1ac.VolumeMountApplyConfiguration {
	return *corev1ac.VolumeMount().
		WithName(tokenVolumeName).
		WithMountPath(configMountPath).
		WithReadOnly(true)
}

func createTokenVolume(trainJob *trainer.TrainJob) corev1ac.VolumeApplyConfiguration {
	configMapName := fmt.Sprintf("%s-tls-config", trainJob.Name)

	return *corev1ac.Volume().
		WithName(tokenVolumeName).
		WithProjected(
			corev1ac.ProjectedVolumeSource().
				WithSources(
					corev1ac.VolumeProjection().
						WithServiceAccountToken(
							corev1ac.ServiceAccountTokenProjection().
								WithAudience(statusserver.TokenAudience(trainJob.Namespace, trainJob.Name)).
								WithExpirationSeconds(tokenExpirySeconds).
								WithPath(tokenFileName),
						),
					corev1ac.VolumeProjection().
						WithConfigMap(
							corev1ac.ConfigMapProjection().
								WithName(configMapName).
								WithItems(
									corev1ac.KeyToPath().
										WithKey(caCertKey).
										WithPath(caCertFileName),
								),
						),
				),
		)
}

// buildStatusServerCaCrtConfigMap creates a ConfigMap that will copy the ca.crt from the webhook secret
func (p *Status) buildStatusServerCaCrtConfigMap(ctx context.Context, trainJob *trainer.TrainJob) (*corev1ac.ConfigMapApplyConfiguration, error) {
	configMapName := fmt.Sprintf("%s-tls-config", trainJob.Name)

	// Get the CA cert from the webhook secret
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: cert.GetOperatorNamespace(),
		Name:      p.cfg.CertManagement.WebhookSecretName,
	}

	var caCertData string
	if err := p.client.Get(ctx, secretKey, secret); err == nil {
		if caCert, ok := secret.Data[caCertKey]; ok && len(caCert) > 0 {
			caCertData = string(caCert)
		} else {
			return nil, fmt.Errorf("failed to find status server ca.crt in tls secret")
		}
	} else {
		return nil, fmt.Errorf("failed to look up status server tls secret: %w", err)
	}

	configMap := corev1ac.ConfigMap(configMapName, trainJob.Namespace).
		WithData(map[string]string{
			caCertKey: caCertData,
		}).
		WithOwnerReferences(
			metav1ac.OwnerReference().
				WithAPIVersion(trainer.GroupVersion.String()).
				WithKind(trainer.TrainJobKind).
				WithName(trainJob.Name).
				WithUID(trainJob.UID).
				WithController(true).
				WithBlockOwnerDeletion(true),
		)

	return configMap, nil
}
