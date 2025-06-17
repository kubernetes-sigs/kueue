package rayservice

import (
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/kueue/pkg/controller/constants"
)

type ServiceWrapper struct{ rayv1.RayService }

func serveConfigV2Template(serveAppName string) string {
	return fmt.Sprintf(`
    applications:
    - name: %s
      import_path: fruit.deployment_graph
      route_prefix: /fruit
      runtime_env:
        working_dir: "https://github.com/ray-project/test_dag/archive/41d09119cbdf8450599f993f51318e9e27c59098.zip"
      deployments:
        - name: MangoStand
          num_replicas: 1
          user_config:
            price: 3
          ray_actor_options:
            num_cpus: 0.1
        - name: OrangeStand
          num_replicas: 1
          user_config:
            price: 2
          ray_actor_options:
            num_cpus: 0.1
        - name: PearStand
          num_replicas: 1
          user_config:
            price: 1
          ray_actor_options:
            num_cpus: 0.1`, serveAppName)
}

// MakeService creates a wrapper for rayService
func MakeService(name, ns string) *ServiceWrapper {
	serveConfigV2 := serveConfigV2Template("test-rayservice")
	return &ServiceWrapper{rayv1.RayService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},

		Spec: rayv1.RayServiceSpec{
			ServeConfigV2: serveConfigV2,
			RayClusterSpec: rayv1.RayClusterSpec{
				Suspend: ptr.To(true),
				HeadGroupSpec: rayv1.HeadGroupSpec{
					RayStartParams: map[string]string{},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: "Never",
							Containers: []corev1.Container{
								{
									Name:    "head-container",
									Command: []string{},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{},
										Limits:   corev1.ResourceList{},
									},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName:      "workers-group-0",
						Replicas:       ptr.To[int32](1),
						MinReplicas:    ptr.To[int32](0),
						MaxReplicas:    ptr.To[int32](10),
						RayStartParams: map[string]string{},
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: "Never",
								Containers: []corev1.Container{
									{
										Name:    "worker-container",
										Command: []string{},
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{},
											Limits:   corev1.ResourceList{},
										},
									},
								},
								NodeSelector: map[string]string{},
							},
						},
					},
				},
			},
		},
	}}
}

// NodeSelectorHeadGroup adds a node selector to the job's head.
func (j *ServiceWrapper) NodeSelectorHeadGroup(k, v string) *ServiceWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector[k] = v
	return j
}

// Obj returns the inner Job.
func (j *ServiceWrapper) Obj() *rayv1.RayService {
	return &j.RayService
}

// Suspend updates the suspend status of the job
func (j *ServiceWrapper) Suspend(s bool) *ServiceWrapper {
	j.Spec.RayClusterSpec.Suspend = &s
	return j
}

func (j *ServiceWrapper) RequestWorkerGroup(name corev1.ResourceName, quantity string) *ServiceWrapper {
	c := &j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

func (j *ServiceWrapper) RequestHead(name corev1.ResourceName, quantity string) *ServiceWrapper {
	c := &j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

// Queue updates the queue name of the job
func (j *ServiceWrapper) Queue(queue string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Clone returns deep copy of the Job.
func (j *ServiceWrapper) Clone() *ServiceWrapper {
	return &ServiceWrapper{RayService: *j.DeepCopy()}
}

func (j *ServiceWrapper) WithEnableAutoscaling(value *bool) *ServiceWrapper {
	j.Spec.RayClusterSpec.EnableInTreeAutoscaling = value
	return j
}

func (j *ServiceWrapper) WithWorkerGroups(workers ...rayv1.WorkerGroupSpec) *ServiceWrapper {
	j.Spec.RayClusterSpec.WorkerGroupSpecs = workers
	return j
}

func (j *ServiceWrapper) WithHeadGroupSpec(value rayv1.HeadGroupSpec) *ServiceWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec = value
	return j
}

func (j *ServiceWrapper) WithPriorityClassName(value string) *ServiceWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.PriorityClassName = value
	return j
}

func (j *ServiceWrapper) WithWorkerPriorityClassName(value string) *ServiceWrapper {
	j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.PriorityClassName = value
	return j
}

func (j *ServiceWrapper) WithNumOfHosts(groupName string, value int32) *ServiceWrapper {
	for index, group := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		if group.GroupName == groupName {
			j.Spec.RayClusterSpec.WorkerGroupSpecs[index].NumOfHosts = value
		}
	}
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *ServiceWrapper) WorkloadPriorityClass(wpc string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Label sets the label key and value
func (j *ServiceWrapper) Label(key, value string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// NodeLabel sets the label key and value for specific RayNodeType
func (j *ServiceWrapper) NodeLabel(rayType rayv1.RayNodeType, key, value string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		if j.Spec.RayClusterSpec.HeadGroupSpec.Template.Labels == nil {
			j.Spec.RayClusterSpec.HeadGroupSpec.Template.Labels = make(map[string]string)
		}
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Labels[key] = value
	case rayv1.WorkerNode:
		if j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Labels == nil {
			j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Labels = make(map[string]string)
		}
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Labels[key] = value
	}
	return j
}

// StatusConditions adds a condition
func (j *ServiceWrapper) StatusConditions(c metav1.Condition) *ServiceWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

// ManagedBy adds a managedby.
func (j *ServiceWrapper) ManagedBy(c string) *ServiceWrapper {
	j.Spec.RayClusterSpec.ManagedBy = &c
	return j
}

// Request adds a resource request to the default container.
func (j *ServiceWrapper) Request(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	}
	return j
}

// Limit adds a resource limit to the default container.
func (j *ServiceWrapper) Limit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	}
	return j
}

// RequestAndLimit adds a resource request and limit to the default container.
func (j *ServiceWrapper) RequestAndLimit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	return j.Request(rayType, r, v).Limit(rayType, r, v)
}

func (j *ServiceWrapper) Image(rayType rayv1.RayNodeType, image string, args []string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Args = args
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Args = args
	}
	return j
}

func (j *ServiceWrapper) RayVersion(rv string) *ServiceWrapper {
	j.Spec.RayClusterSpec.RayVersion = rv
	return j
}
