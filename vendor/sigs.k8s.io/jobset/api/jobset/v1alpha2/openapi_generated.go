//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2023 The Kubernetes Authors.
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
// Code generated by openapi-gen. DO NOT EDIT.

package v1alpha2

import (
	common "k8s.io/kube-openapi/pkg/common"
	spec "k8s.io/kube-openapi/pkg/validation/spec"
)

func GetOpenAPIDefinitions(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
	return map[string]common.OpenAPIDefinition{
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.Coordinator":         schema_jobset_api_jobset_v1alpha2_Coordinator(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.DependsOn":           schema_jobset_api_jobset_v1alpha2_DependsOn(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicy":       schema_jobset_api_jobset_v1alpha2_FailurePolicy(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicyRule":   schema_jobset_api_jobset_v1alpha2_FailurePolicyRule(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSet":              schema_jobset_api_jobset_v1alpha2_JobSet(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetList":          schema_jobset_api_jobset_v1alpha2_JobSetList(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetSpec":          schema_jobset_api_jobset_v1alpha2_JobSetSpec(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetStatus":        schema_jobset_api_jobset_v1alpha2_JobSetStatus(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.Network":             schema_jobset_api_jobset_v1alpha2_Network(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJob":       schema_jobset_api_jobset_v1alpha2_ReplicatedJob(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJobStatus": schema_jobset_api_jobset_v1alpha2_ReplicatedJobStatus(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.StartupPolicy":       schema_jobset_api_jobset_v1alpha2_StartupPolicy(ref),
		"sigs.k8s.io/jobset/api/jobset/v1alpha2.SuccessPolicy":       schema_jobset_api_jobset_v1alpha2_SuccessPolicy(ref),
	}
}

func schema_jobset_api_jobset_v1alpha2_Coordinator(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "Coordinator defines which pod can be marked as the coordinator for the JobSet workload.",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"replicatedJob": {
						SchemaProps: spec.SchemaProps{
							Description: "ReplicatedJob is the name of the ReplicatedJob which contains the coordinator pod.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"jobIndex": {
						SchemaProps: spec.SchemaProps{
							Description: "JobIndex is the index of Job which contains the coordinator pod (i.e., for a ReplicatedJob with N replicas, there are Job indexes 0 to N-1).",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"podIndex": {
						SchemaProps: spec.SchemaProps{
							Description: "PodIndex is the Job completion index of the coordinator pod.",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
				},
				Required: []string{"replicatedJob"},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_DependsOn(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "DependsOn defines the dependency on the previous ReplicatedJob status.",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"name": {
						SchemaProps: spec.SchemaProps{
							Description: "Name of the previous ReplicatedJob.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"status": {
						SchemaProps: spec.SchemaProps{
							Description: "Status defines the condition for the ReplicatedJob. Only Ready or Complete status can be set.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
				},
				Required: []string{"name", "status"},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_FailurePolicy(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"maxRestarts": {
						SchemaProps: spec.SchemaProps{
							Description: "MaxRestarts defines the limit on the number of JobSet restarts. A restart is achieved by recreating all active child jobs.",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"restartStrategy": {
						SchemaProps: spec.SchemaProps{
							Description: "RestartStrategy defines the strategy to use when restarting the JobSet. Defaults to Recreate.",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"rules": {
						SchemaProps: spec.SchemaProps{
							Description: "List of failure policy rules for this JobSet. For a given Job failure, the rules will be evaluated in order, and only the first matching rule will be executed. If no matching rule is found, the RestartJobSet action is applied.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicyRule"),
									},
								},
							},
						},
					},
				},
			},
		},
		Dependencies: []string{
			"sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicyRule"},
	}
}

func schema_jobset_api_jobset_v1alpha2_FailurePolicyRule(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "FailurePolicyRule defines a FailurePolicyAction to be executed if a child job fails due to a reason listed in OnJobFailureReasons.",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"name": {
						SchemaProps: spec.SchemaProps{
							Description: "The name of the failure policy rule. The name is defaulted to 'failurePolicyRuleN' where N is the index of the failure policy rule. The name must match the regular expression \"^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$\".",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"action": {
						SchemaProps: spec.SchemaProps{
							Description: "The action to take if the rule is matched.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"onJobFailureReasons": {
						SchemaProps: spec.SchemaProps{
							Description: "The requirement on the job failure reasons. The requirement is satisfied if at least one reason matches the list. The rules are evaluated in order, and the first matching rule is executed. An empty list applies the rule to any job failure reason.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: "",
										Type:    []string{"string"},
										Format:  "",
									},
								},
							},
						},
					},
					"targetReplicatedJobs": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-type": "atomic",
							},
						},
						SchemaProps: spec.SchemaProps{
							Description: "TargetReplicatedJobs are the names of the replicated jobs the operator applies to. An empty list will apply to all replicatedJobs.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: "",
										Type:    []string{"string"},
										Format:  "",
									},
								},
							},
						},
					},
				},
				Required: []string{"name", "action"},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_JobSet(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "JobSet is the Schema for the jobsets API",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"kind": {
						SchemaProps: spec.SchemaProps{
							Description: "Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"apiVersion": {
						SchemaProps: spec.SchemaProps{
							Description: "APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"metadata": {
						SchemaProps: spec.SchemaProps{
							Default: map[string]interface{}{},
							Ref:     ref("k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta"),
						},
					},
					"spec": {
						SchemaProps: spec.SchemaProps{
							Default: map[string]interface{}{},
							Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetSpec"),
						},
					},
					"status": {
						SchemaProps: spec.SchemaProps{
							Default: map[string]interface{}{},
							Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetStatus"),
						},
					},
				},
			},
		},
		Dependencies: []string{
			"k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta", "sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetSpec", "sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSetStatus"},
	}
}

func schema_jobset_api_jobset_v1alpha2_JobSetList(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "JobSetList contains a list of JobSet",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"kind": {
						SchemaProps: spec.SchemaProps{
							Description: "Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"apiVersion": {
						SchemaProps: spec.SchemaProps{
							Description: "APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"metadata": {
						SchemaProps: spec.SchemaProps{
							Default: map[string]interface{}{},
							Ref:     ref("k8s.io/apimachinery/pkg/apis/meta/v1.ListMeta"),
						},
					},
					"items": {
						SchemaProps: spec.SchemaProps{
							Type: []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSet"),
									},
								},
							},
						},
					},
				},
				Required: []string{"items"},
			},
		},
		Dependencies: []string{
			"k8s.io/apimachinery/pkg/apis/meta/v1.ListMeta", "sigs.k8s.io/jobset/api/jobset/v1alpha2.JobSet"},
	}
}

func schema_jobset_api_jobset_v1alpha2_JobSetSpec(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "JobSetSpec defines the desired state of JobSet",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"replicatedJobs": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-map-keys": []interface{}{
									"name",
								},
								"x-kubernetes-list-type": "map",
							},
						},
						SchemaProps: spec.SchemaProps{
							Description: "ReplicatedJobs is the group of jobs that will form the set.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJob"),
									},
								},
							},
						},
					},
					"network": {
						SchemaProps: spec.SchemaProps{
							Description: "Network defines the networking options for the jobset.",
							Ref:         ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.Network"),
						},
					},
					"successPolicy": {
						SchemaProps: spec.SchemaProps{
							Description: "SuccessPolicy configures when to declare the JobSet as succeeded. The JobSet is always declared succeeded if all jobs in the set finished with status complete.",
							Ref:         ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.SuccessPolicy"),
						},
					},
					"failurePolicy": {
						SchemaProps: spec.SchemaProps{
							Description: "FailurePolicy, if set, configures when to declare the JobSet as failed. The JobSet is always declared failed if any job in the set finished with status failed.",
							Ref:         ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicy"),
						},
					},
					"startupPolicy": {
						SchemaProps: spec.SchemaProps{
							Description: "StartupPolicy, if set, configures in what order jobs must be started Deprecated: StartupPolicy is deprecated, please use the DependsOn API.",
							Ref:         ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.StartupPolicy"),
						},
					},
					"suspend": {
						SchemaProps: spec.SchemaProps{
							Description: "Suspend suspends all running child Jobs when set to true.",
							Type:        []string{"boolean"},
							Format:      "",
						},
					},
					"coordinator": {
						SchemaProps: spec.SchemaProps{
							Description: "Coordinator can be used to assign a specific pod as the coordinator for the JobSet. If defined, an annotation will be added to all Jobs and pods with coordinator pod, which contains the stable network endpoint where the coordinator pod can be reached. jobset.sigs.k8s.io/coordinator=<pod hostname>.<headless service>",
							Ref:         ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.Coordinator"),
						},
					},
					"managedBy": {
						SchemaProps: spec.SchemaProps{
							Description: "ManagedBy is used to indicate the controller or entity that manages a JobSet. The built-in JobSet controller reconciles JobSets which don't have this field at all or the field value is the reserved string `jobset.sigs.k8s.io/jobset-controller`, but skips reconciling JobSets with a custom value for this field.\n\nThe value must be a valid domain-prefixed path (e.g. acme.io/foo) - all characters before the first \"/\" must be a valid subdomain as defined by RFC 1123. All characters trailing the first \"/\" must be valid HTTP Path characters as defined by RFC 3986. The value cannot exceed 63 characters. The field is immutable.",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"ttlSecondsAfterFinished": {
						SchemaProps: spec.SchemaProps{
							Description: "TTLSecondsAfterFinished limits the lifetime of a JobSet that has finished execution (either Complete or Failed). If this field is set, TTLSecondsAfterFinished after the JobSet finishes, it is eligible to be automatically deleted. When the JobSet is being deleted, its lifecycle guarantees (e.g. finalizers) will be honored. If this field is unset, the JobSet won't be automatically deleted. If this field is set to zero, the JobSet becomes eligible to be deleted immediately after it finishes.",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
				},
			},
		},
		Dependencies: []string{
			"sigs.k8s.io/jobset/api/jobset/v1alpha2.Coordinator", "sigs.k8s.io/jobset/api/jobset/v1alpha2.FailurePolicy", "sigs.k8s.io/jobset/api/jobset/v1alpha2.Network", "sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJob", "sigs.k8s.io/jobset/api/jobset/v1alpha2.StartupPolicy", "sigs.k8s.io/jobset/api/jobset/v1alpha2.SuccessPolicy"},
	}
}

func schema_jobset_api_jobset_v1alpha2_JobSetStatus(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "JobSetStatus defines the observed state of JobSet",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"conditions": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-map-keys": []interface{}{
									"type",
								},
								"x-kubernetes-list-type": "map",
							},
						},
						SchemaProps: spec.SchemaProps{
							Type: []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("k8s.io/apimachinery/pkg/apis/meta/v1.Condition"),
									},
								},
							},
						},
					},
					"restarts": {
						SchemaProps: spec.SchemaProps{
							Description: "Restarts tracks the number of times the JobSet has restarted (i.e. recreated in case of RecreateAll policy).",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"restartsCountTowardsMax": {
						SchemaProps: spec.SchemaProps{
							Description: "RestartsCountTowardsMax tracks the number of times the JobSet has restarted that counts towards the maximum allowed number of restarts.",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"terminalState": {
						SchemaProps: spec.SchemaProps{
							Description: "TerminalState the state of the JobSet when it finishes execution. It can be either Complete or Failed. Otherwise, it is empty by default.",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"replicatedJobsStatus": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-map-keys": []interface{}{
									"name",
								},
								"x-kubernetes-list-type": "map",
							},
						},
						SchemaProps: spec.SchemaProps{
							Description: "ReplicatedJobsStatus track the number of JobsReady for each replicatedJob.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJobStatus"),
									},
								},
							},
						},
					},
				},
			},
		},
		Dependencies: []string{
			"k8s.io/apimachinery/pkg/apis/meta/v1.Condition", "sigs.k8s.io/jobset/api/jobset/v1alpha2.ReplicatedJobStatus"},
	}
}

func schema_jobset_api_jobset_v1alpha2_Network(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"enableDNSHostnames": {
						SchemaProps: spec.SchemaProps{
							Description: "EnableDNSHostnames allows pods to be reached via their hostnames. Pods will be reachable using the fully qualified pod hostname: <jobSet.name>-<spec.replicatedJob.name>-<job-index>-<pod-index>.<subdomain>",
							Type:        []string{"boolean"},
							Format:      "",
						},
					},
					"subdomain": {
						SchemaProps: spec.SchemaProps{
							Description: "Subdomain is an explicit choice for a network subdomain name When set, any replicated job in the set is added to this network. Defaults to <jobSet.name> if not set.",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"publishNotReadyAddresses": {
						SchemaProps: spec.SchemaProps{
							Description: "Indicates if DNS records of pods should be published before the pods are ready. Defaults to True.",
							Type:        []string{"boolean"},
							Format:      "",
						},
					},
				},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_ReplicatedJob(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"name": {
						SchemaProps: spec.SchemaProps{
							Description: "Name is the name of the entry and will be used as a suffix for the Job name.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"template": {
						SchemaProps: spec.SchemaProps{
							Description: "Template defines the template of the Job that will be created.",
							Default:     map[string]interface{}{},
							Ref:         ref("k8s.io/api/batch/v1.JobTemplateSpec"),
						},
					},
					"replicas": {
						SchemaProps: spec.SchemaProps{
							Description: "Replicas is the number of jobs that will be created from this ReplicatedJob's template. Jobs names will be in the format: <jobSet.name>-<spec.replicatedJob.name>-<job-index>",
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"dependsOn": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-map-keys": []interface{}{
									"name",
								},
								"x-kubernetes-list-type": "map",
							},
						},
						SchemaProps: spec.SchemaProps{
							Description: "DependsOn is an optional list that specifies the preceding ReplicatedJobs upon which the current ReplicatedJob depends. If specified, the ReplicatedJob will be created only after the referenced ReplicatedJobs reach their desired state. The Order of ReplicatedJobs is defined by their enumeration in the slice. Note, that the first ReplicatedJob in the slice cannot use the DependsOn API. Currently, only a single item is supported in the DependsOn list. If JobSet is suspended the all active ReplicatedJobs will be suspended. When JobSet is resumed the Job sequence starts again. This API is mutually exclusive with the StartupPolicy API.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: map[string]interface{}{},
										Ref:     ref("sigs.k8s.io/jobset/api/jobset/v1alpha2.DependsOn"),
									},
								},
							},
						},
					},
				},
				Required: []string{"name", "template"},
			},
		},
		Dependencies: []string{
			"k8s.io/api/batch/v1.JobTemplateSpec", "sigs.k8s.io/jobset/api/jobset/v1alpha2.DependsOn"},
	}
}

func schema_jobset_api_jobset_v1alpha2_ReplicatedJobStatus(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Description: "ReplicatedJobStatus defines the observed ReplicatedJobs Readiness.",
				Type:        []string{"object"},
				Properties: map[string]spec.Schema{
					"name": {
						SchemaProps: spec.SchemaProps{
							Description: "Name of the ReplicatedJob.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"ready": {
						SchemaProps: spec.SchemaProps{
							Description: "Ready is the number of child Jobs where the number of ready pods and completed pods is greater than or equal to the total expected pod count for the Job (i.e., the minimum of job.spec.parallelism and job.spec.completions).",
							Default:     0,
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"succeeded": {
						SchemaProps: spec.SchemaProps{
							Description: "Succeeded is the number of successfully completed child Jobs.",
							Default:     0,
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"failed": {
						SchemaProps: spec.SchemaProps{
							Description: "Failed is the number of failed child Jobs.",
							Default:     0,
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"active": {
						SchemaProps: spec.SchemaProps{
							Description: "Active is the number of child Jobs with at least 1 pod in a running or pending state which are not marked for deletion.",
							Default:     0,
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
					"suspended": {
						SchemaProps: spec.SchemaProps{
							Description: "Suspended is the number of child Jobs which are in a suspended state.",
							Default:     0,
							Type:        []string{"integer"},
							Format:      "int32",
						},
					},
				},
				Required: []string{"name", "ready", "succeeded", "failed", "active", "suspended"},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_StartupPolicy(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"startupPolicyOrder": {
						SchemaProps: spec.SchemaProps{
							Description: "StartupPolicyOrder determines the startup order of the ReplicatedJobs. AnyOrder means to start replicated jobs in any order. InOrder means to start them as they are listed in the JobSet. A ReplicatedJob is started only when all the jobs of the previous one are ready.",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
				},
				Required: []string{"startupPolicyOrder"},
			},
		},
	}
}

func schema_jobset_api_jobset_v1alpha2_SuccessPolicy(ref common.ReferenceCallback) common.OpenAPIDefinition {
	return common.OpenAPIDefinition{
		Schema: spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"operator": {
						SchemaProps: spec.SchemaProps{
							Description: "Operator determines either All or Any of the selected jobs should succeed to consider the JobSet successful",
							Default:     "",
							Type:        []string{"string"},
							Format:      "",
						},
					},
					"targetReplicatedJobs": {
						VendorExtensible: spec.VendorExtensible{
							Extensions: spec.Extensions{
								"x-kubernetes-list-type": "atomic",
							},
						},
						SchemaProps: spec.SchemaProps{
							Description: "TargetReplicatedJobs are the names of the replicated jobs the operator will apply to. A null or empty list will apply to all replicatedJobs.",
							Type:        []string{"array"},
							Items: &spec.SchemaOrArray{
								Schema: &spec.Schema{
									SchemaProps: spec.SchemaProps{
										Default: "",
										Type:    []string{"string"},
										Format:  "",
									},
								},
							},
						},
					},
				},
				Required: []string{"operator"},
			},
		},
	}
}
