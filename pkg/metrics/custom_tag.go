package metrics

import (
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kueue/apis/config/v1beta1"
)

var customTagsConf = CustomTagsConfiguration{}

type CustomTagsConfiguration struct {
	ClusterQueue CustomTagsObjectConfiguration
	LocalQueue   CustomTagsObjectConfiguration
}

type CustomTagsObjectConfiguration struct {
	ResourceTags []string
	MetricTags   []string
}

func getPrometheusTag(tag v1beta1.CustomMetricTag) string {
	if tag.OverrideMetricTag != nil {
		return *tag.OverrideMetricTag
	}
	return tag.ResourceTag
}

type Metric[T any] struct {
	Name                     string
	Help                     string
	StandardLabels           []string
	Buckets                  []float64
	globalVariable           **T
	clusterQueueCustomLabels bool
	localQueueCustomLabels   bool
}

type MetricsGroup[T any] struct {
	metrics  []Metric[T]
	initFunc func(Metric[T], []string) *T
	once     sync.Once
}

func (m *MetricsGroup[T]) Metrics() []Metric[T] {
	return m.metrics
}

func (m *MetricsGroup[T]) InitFunc() func(Metric[T], []string) *T {
	return m.initFunc
}

func (m *MetricsGroup[T]) init() {
	for i, metric := range m.metrics {
		labels := metric.StandardLabels
		if metric.clusterQueueCustomLabels {
			labels = append(metric.StandardLabels, customTagsConf.ClusterQueue.MetricTags...)
		}
		if metric.localQueueCustomLabels {
			labels = append(metric.StandardLabels, customTagsConf.LocalQueue.MetricTags...)
		}
		*m.metrics[i].globalVariable = m.initFunc(metric, labels)
	}
}

func getResourceTagValues(cq metav1.Object, customTags CustomTagsObjectConfiguration) []string {
	tags := []string{}
	for _, tag := range customTags.ResourceTags {
		t, ok := cq.GetLabels()[tag]
		if !ok {
			t = cq.GetAnnotations()[tag]
		}
		tags = append(tags, t)
	}
	return tags
}

func getCustomTagsObjectConfiguration(customMetricTags []v1beta1.CustomMetricTag) CustomTagsObjectConfiguration {
	customTagsObjectConf := CustomTagsObjectConfiguration{
		ResourceTags: make([]string, 0, len(customMetricTags)),
		MetricTags:   make([]string, 0, len(customMetricTags)),
	}
	for _, t := range customMetricTags {
		customTagsObjectConf.ResourceTags = append(customTagsObjectConf.ResourceTags, t.ResourceTag)
		customTagsObjectConf.MetricTags = append(customTagsObjectConf.MetricTags, getPrometheusTag(t))
	}
	return customTagsObjectConf
}

func getConfiguration(customMetricTags *v1beta1.CustomMetricTags) CustomTagsConfiguration {
	if customMetricTags == nil {
		return CustomTagsConfiguration{}
	}
	return CustomTagsConfiguration{
		ClusterQueue: getCustomTagsObjectConfiguration(customMetricTags.ClusterQueue),
		LocalQueue:   getCustomTagsObjectConfiguration(customMetricTags.LocalQueue),
	}
}

func (m *MetricsGroup[T]) Init() {
	m.once.Do(m.init)
}

func initCustomTagsMetric(customMetricsTagsConfiguration *v1beta1.CustomMetricTags) {
	customTagsConf = getConfiguration(customMetricsTagsConfiguration)

	customTagsCounterMetrics.Init()
	customTagsGaugeMetrics.Init()
	customTagsHistogramMetrics.Init()
}
