package deployment

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	errDeploymentOptsTypeAssertion = errors.New("options are not of type DeploymentIntegrationOptions")
	errDeploymentOptsNotFound      = errors.New("deploymentIntegrationOptions not found in options")
)

type Webhook struct {
	client                     client.Client
	manageJobsWithoutQueueName bool
	namespaceSelector          *metav1.LabelSelector
	deploymentSelector         *metav1.LabelSelector
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	deploymentOpts, err := getDeploymentOptions(options.IntegrationOptions)
	if err != nil {
		return err
	}
	wh := &Webhook{
		client:                     mgr.GetClient(),
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		namespaceSelector:          deploymentOpts.NamespaceSelector,
		deploymentSelector:         deploymentOpts.DeploymentSelector,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&appsv1.Deployment{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

func getDeploymentOptions(integrationOpts map[string]any) (configapi.DeploymentIntegrationOptions, error) {
	opts, ok := integrationOpts[corev1.SchemeGroupVersion.WithKind("Deployment").String()]
	if !ok {
		return configapi.DeploymentIntegrationOptions{}, errDeploymentOptsNotFound
	}
	deploymentOpts, ok := opts.(*configapi.DeploymentIntegrationOptions)
	if !ok {
		return configapi.DeploymentIntegrationOptions{}, fmt.Errorf("%w, got %T", errDeploymentOptsTypeAssertion, opts)
	}
	return *deploymentOpts, nil
}

// +kubebuilder:webhook:path=/mutate--v1-deployment,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=deployments,verbs=create,versions=v1,name=mdeployment.kb.io,admissionReviewVersions=v1
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

var _ webhook.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	d := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("deployment-webhook").WithValues("deployment", klog.KObj(d))
	log.V(5).Info("Applying defaults")

	// Check for deployment label selector match
	deploymentSelector, err := metav1.LabelSelectorAsSelector(wh.deploymentSelector)
	if err != nil {
		return fmt.Errorf("failed to create deployment selector: %w", err)
	}
	if !deploymentSelector.Matches(labels.Set(d.Labels)) {
		return nil
	}

	// Get deployment namespace and check for namespace label selector match
	ns := &corev1.Namespace{}
	if err := wh.client.Get(ctx, client.ObjectKey{Name: d.Namespace}, ns); err != nil {
		return fmt.Errorf("failed to run mutating webhook on deployment %s/%s, error while getting namespace: %w",
			d.Namespace,
			d.Name,
			err,
		)
	}
	log.V(5).Info("Found deployment namespace", "Namespace.Name", ns.GetName())
	nsSelector, err := metav1.LabelSelectorAsSelector(wh.namespaceSelector)
	if err != nil {
		return fmt.Errorf("failed to parse namespace selector: %w", err)
	}
	if !nsSelector.Matches(labels.Set(ns.GetLabels())) {
		return nil
	}

	if d.Spec.Template.Labels == nil {
		d.Spec.Template.Labels = make(map[string]string)
	}
	d.Spec.Template.Labels[constants.QueueLabel] = d.Labels[constants.QueueLabel]

	return nil
}

// +kubebuilder:webhook:path=/validate--v1-deployment,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=deployments,verbs=create;update,versions=v1,name=vdeployment.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

var (
	deploymentLabelsPath         = field.NewPath("metadata", "labels")
	deploymentQueueNameLabelPath = deploymentLabelsPath.Key(constants.QueueLabel)

	podSpecLabelsPath         = field.NewPath("spec", "template", "metadata", "labels")
	podSpecQueueNameLabelPath = podSpecLabelsPath.Key(constants.QueueLabel)
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldDeployment := FromObject(oldObj)
	newDeployment := FromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("deployment-webhook").WithValues("deployment", klog.KObj(newDeployment))
	log.V(5).Info("Validating update")
	allErrs := apivalidation.ValidateImmutableField(
		newDeployment.GetLabels()[constants.QueueLabel],
		oldDeployment.GetLabels()[constants.QueueLabel],
		deploymentQueueNameLabelPath,
	)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newDeployment.Spec.Template.GetLabels()[constants.QueueLabel],
		oldDeployment.Spec.Template.GetLabels()[constants.QueueLabel],
		podSpecQueueNameLabelPath,
	)...)

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}
