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

package recorder

import (
	"context"
	"encoding/csv"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/generator"
)

type CQEvent struct {
	Time      time.Time
	Name      string
	ClassName string
	Cohort    string
	UID       types.UID

	CPUReservation     int64
	CPUUsage           int64
	CPUQuota           int64
	PendingWorkloads   int32
	ReservingWorkloads int32
	AdmittedWorkloads  int32
	Active             bool
}

type CQState struct {
	FirstEventTime  time.Time
	FirstActiveTime time.Time
	CPUUsed         int64
	CPUMaxUsage     int64
	LastEvent       *CQEvent
}

var CQStateCsvHeader = []string{
	"name",
	"cohort",
	"class name",
	"CPU quota (mCPU)",
	"CPU used (mCPU * ms)",
	"CPU max usage (mCPU)",
	"monitor time (ms)",
}

func (cqs *CQState) CsvRecord() []string {
	monitoringTimeMs := cqs.LastEvent.Time.Sub(cqs.FirstEventTime).Milliseconds()
	return []string{
		cqs.LastEvent.Name,
		cqs.LastEvent.Cohort,
		cqs.LastEvent.ClassName,
		strconv.FormatInt(cqs.LastEvent.CPUQuota, 10),
		strconv.FormatInt(cqs.CPUUsed, 10),
		strconv.FormatInt(cqs.CPUMaxUsage, 10),
		strconv.FormatInt(monitoringTimeMs, 10),
	}
}

type CQStore map[string]*CQState

type WLEvent struct {
	Time time.Time
	types.NamespacedName
	UID       types.UID
	ClassName string
	Admitted  bool
	Evicted   bool
	Finished  bool
}

type WLState struct {
	ID int
	types.NamespacedName
	ClassName        string
	FirstEventTime   time.Time
	TimeToAdmitMs    int64
	TimeToFinishedMs int64
	EvictionCount    int32
	LastEvent        *WLEvent
}

var WLStateCsvHeader = []string{
	"id",
	"class name",
	"namespace",
	"name",
	"ms to admitted",
	"ms to finish",
	"num evictions",
}

func (wls *WLState) CsvRecord() []string {
	return []string{
		strconv.Itoa(wls.ID),
		wls.ClassName,
		wls.Namespace,
		wls.Name,
		strconv.FormatInt(wls.TimeToAdmitMs, 10),
		strconv.FormatInt(wls.TimeToFinishedMs, 10),
		strconv.FormatInt(int64(wls.EvictionCount), 10),
	}
}

type WLStore map[types.UID]*WLState

type Store struct {
	CQ CQStore
	WL WLStore
}

type Recorder struct {
	maxRecording time.Duration
	running      atomic.Bool
	cqEvChan     chan *CQEvent
	wlEvChan     chan *WLEvent

	Store Store
}

func New(maxRecording time.Duration) *Recorder {
	return &Recorder{
		maxRecording: maxRecording,
		running:      atomic.Bool{},
		cqEvChan:     make(chan *CQEvent, 10),
		wlEvChan:     make(chan *WLEvent, 10),
		Store: Store{
			CQ: make(CQStore),
			WL: make(WLStore),
		},
	}
}

func (r *Recorder) recordCQEvent(ev *CQEvent) {
	state, found := r.Store.CQ[ev.Name]
	if !found {
		state = &CQState{
			FirstEventTime: ev.Time,
			LastEvent:      ev,
		}
		r.Store.CQ[ev.Name] = state
	} else {
		if state.LastEvent.CPUUsage > 0 {
			state.CPUUsed += state.LastEvent.CPUUsage * ev.Time.Sub(state.LastEvent.Time).Milliseconds()
		}
		state.LastEvent = ev
	}

	if ev.Active && state.FirstActiveTime.IsZero() {
		state.FirstActiveTime = ev.Time
	}
	state.CPUMaxUsage = max(state.CPUMaxUsage, ev.CPUUsage)
}

func (r *Recorder) recordWLEvent(ev *WLEvent) {
	state, found := r.Store.WL[ev.UID]
	if !found {
		state = &WLState{
			ID:             len(r.Store.WL),
			NamespacedName: ev.NamespacedName,
			ClassName:      ev.ClassName,
			FirstEventTime: ev.Time,
			LastEvent:      &WLEvent{},
		}
		r.Store.WL[ev.UID] = state
	}

	if ev.Admitted && !state.LastEvent.Admitted {
		state.TimeToAdmitMs = ev.Time.Sub(state.FirstEventTime).Milliseconds()
	}

	if ev.Evicted && !state.LastEvent.Evicted {
		state.EvictionCount++
	}

	if ev.Finished && !state.LastEvent.Finished {
		state.TimeToFinishedMs = ev.Time.Sub(state.FirstEventTime).Milliseconds()
	}

	state.LastEvent = ev
}

func (r *Recorder) expectMoreEvents() bool {
	for _, cqStatus := range r.Store.CQ {
		s := cqStatus.LastEvent
		if (s.PendingWorkloads > 0 || s.ReservingWorkloads > 0) && s.Active {
			return true
		}
	}
	return false
}

type CQGroupSummary struct {
	CPUUsed         int64     `json:"cpuUsed"`
	CPUAverageUsage int64     `json:"cpuAverageUsage"`
	NominalQuota    int64     `json:"nominalQuota"`
	FirstEventTime  time.Time `json:"firstEventTime"`
	LastEventTime   time.Time `json:"lastEventTime"`
}

func (qgs *CQGroupSummary) AddQueueSummary(qs *CQState) {
	qgs.CPUUsed += qs.CPUUsed
	qgs.NominalQuota += qs.LastEvent.CPUQuota
	if qs.FirstEventTime.Before(qgs.FirstEventTime) {
		qgs.FirstEventTime = qs.FirstEventTime
	}
	if qs.LastEvent.Time.After(qgs.LastEventTime) {
		qgs.LastEventTime = qs.LastEvent.Time
	}
}

func (qgs *CQGroupSummary) refreshAverage() {
	monitoringTime := qgs.LastEventTime.Sub(qgs.FirstEventTime).Milliseconds()
	if monitoringTime > 0 {
		qgs.CPUAverageUsage = qgs.CPUUsed / monitoringTime
	}
}

func newCQGroupSummary(qs *CQState) *CQGroupSummary {
	ret := &CQGroupSummary{
		CPUUsed:         qs.CPUUsed,
		CPUAverageUsage: 0,
		NominalQuota:    qs.LastEvent.CPUQuota,
		FirstEventTime:  qs.FirstEventTime,
		LastEventTime:   qs.LastEvent.Time,
	}
	return ret
}

type WorkloadsClassSummary struct {
	Count                    int32 `json:"count"`
	totalTimeToAdmissionMs   int64 `json:"-"`
	totalTimeToFinishMs      int64 `json:"-"`
	TotalEvictions           int32 `json:"totalEvictions"`
	AverageTimeToAdmissionMs int64 `json:"averageTimeToAdmissionMs"`
	AverageTimeToFinishMs    int64 `json:"averageTimeToFinishMs"`
}

func (wcs *WorkloadsClassSummary) refreshAverage() {
	if wcs == nil || wcs.Count == 0 {
		return
	}
	wcs.AverageTimeToAdmissionMs = wcs.totalTimeToAdmissionMs / int64(wcs.Count)
	wcs.AverageTimeToFinishMs = wcs.totalTimeToFinishMs / int64(wcs.Count)
}

type Summary struct {
	ClusterQueueClasses map[string]*CQGroupSummary        `json:"clusterQueueClasses"`
	WorkloadClasses     map[string]*WorkloadsClassSummary `json:"workloadClasses"`
}

func (r *Recorder) WriteSummary(path string) error {
	summary := Summary{
		ClusterQueueClasses: map[string]*CQGroupSummary{},
		WorkloadClasses:     map[string]*WorkloadsClassSummary{},
	}

	for _, cqState := range r.Store.CQ {
		if cqState.LastEvent == nil {
			continue
		}
		if groupSummary, found := summary.ClusterQueueClasses[cqState.LastEvent.ClassName]; found {
			groupSummary.AddQueueSummary(cqState)
		} else {
			summary.ClusterQueueClasses[cqState.LastEvent.ClassName] = newCQGroupSummary(cqState)
		}
	}

	for _, group := range summary.ClusterQueueClasses {
		group.refreshAverage()
	}

	for _, wlState := range r.Store.WL {
		if class, found := summary.WorkloadClasses[wlState.ClassName]; !found {
			summary.WorkloadClasses[wlState.ClassName] = &WorkloadsClassSummary{
				Count:                  1,
				totalTimeToAdmissionMs: wlState.TimeToAdmitMs,
				totalTimeToFinishMs:    wlState.TimeToFinishedMs,
				TotalEvictions:         wlState.EvictionCount,
			}
		} else {
			class.Count++
			class.totalTimeToAdmissionMs += wlState.TimeToAdmitMs
			class.totalTimeToFinishMs += wlState.TimeToFinishedMs
			class.TotalEvictions += wlState.EvictionCount
		}
	}

	for _, class := range summary.WorkloadClasses {
		class.refreshAverage()
	}

	bytes, err := yaml.Marshal(summary)
	if err != nil {
		return err
	}

	return os.WriteFile(path, bytes, 0666)
}

func (r *Recorder) WriteCQCsv(path string) (err error) {
	var f *os.File
	f, err = os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cWriter := csv.NewWriter(f)

	defer func() {
		cWriter.Flush()
		if err == nil {
			err = cWriter.Error()
		}
	}()

	err = cWriter.Write(CQStateCsvHeader)
	if err != nil {
		return err
	}

	for _, cqs := range r.Store.CQ {
		err = cWriter.Write(cqs.CsvRecord())
		if err != nil {
			return err
		}
	}

	return err
}

func (r *Recorder) WriteWLCsv(path string) (err error) {
	var f *os.File
	f, err = os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cWriter := csv.NewWriter(f)

	defer func() {
		cWriter.Flush()
		if err == nil {
			err = cWriter.Error()
		}
	}()

	err = cWriter.Write(WLStateCsvHeader)
	if err != nil {
		return err
	}

	for _, ev := range r.Store.WL {
		err = cWriter.Write(ev.CsvRecord())
		if err != nil {
			return err
		}
	}

	return err
}

func (r *Recorder) Run(ctx context.Context, genDone <-chan struct{}) error {
	r.running.Store(true)
	defer r.running.Store(false)

	generateDone := atomic.Bool{}
	generateDone.Store(false)
	go func() {
		<-genDone
		generateDone.Store(true)
	}()

	ctx, cancel := context.WithTimeout(ctx, r.maxRecording)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-r.cqEvChan:
			r.recordCQEvent(ev)
			if generateDone.Load() && !r.expectMoreEvents() {
				return nil
			}
		case ev := <-r.wlEvChan:
			r.recordWLEvent(ev)
		}
	}
}

func (r *Recorder) RecordWorkloadState(wl *kueue.Workload) {
	if !r.running.Load() {
		return
	}
	r.wlEvChan <- &WLEvent{
		Time: time.Now(),
		NamespacedName: types.NamespacedName{
			Namespace: wl.Namespace,
			Name:      wl.Name,
		},
		UID:       wl.UID,
		ClassName: wl.Labels[generator.ClassLabel],
		Admitted:  apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted),
		Evicted:   apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted),
		Finished:  apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished),
	}
}

func (r *Recorder) RecordCQState(cq *kueue.ClusterQueue) {
	if !r.running.Load() {
		return
	}

	var cpuReserved, cpuUsed, cpuQuota int64
	if len(cq.Status.FlavorsReservation) > 0 && len(cq.Status.FlavorsReservation[0].Resources) > 0 {
		cpuReserved = cq.Status.FlavorsReservation[0].Resources[0].Total.MilliValue()
	}

	if len(cq.Status.FlavorsUsage) > 0 && len(cq.Status.FlavorsUsage[0].Resources) > 0 {
		cpuUsed = cq.Status.FlavorsUsage[0].Resources[0].Total.MilliValue()
	}

	if len(cq.Spec.ResourceGroups) > 0 && len(cq.Spec.ResourceGroups[0].Flavors) > 0 && len(cq.Spec.ResourceGroups[0].Flavors[0].Resources) > 0 {
		cpuQuota = cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota.MilliValue()
	}

	r.cqEvChan <- &CQEvent{
		Time:      time.Now(),
		Name:      cq.Name,
		ClassName: cq.Labels[generator.ClassLabel],
		Cohort:    cq.Spec.Cohort,
		UID:       cq.UID,

		CPUReservation:     cpuReserved,
		CPUUsage:           cpuUsed,
		CPUQuota:           cpuQuota,
		PendingWorkloads:   cq.Status.PendingWorkloads,
		ReservingWorkloads: cq.Status.ReservingWorkloads,
		AdmittedWorkloads:  cq.Status.AdmittedWorkloads,
		Active:             apimeta.IsStatusConditionTrue(cq.Status.Conditions, kueue.AdmissionCheckActive),
	}
}
