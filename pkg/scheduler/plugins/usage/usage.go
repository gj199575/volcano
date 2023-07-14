/*
Copyright 2022 The Volcano Authors.

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

package usage

import (
	"fmt"

	"k8s.io/klog/v2"
	k8sFramework "k8s.io/kubernetes/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/cache"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/metrics/source"
)

const (
	// PluginName indicates name of volcano scheduler plugin.
	PluginName        = "usage"
	cpuUsageAvgPrefix = "CPUUsageAvg."
	memUsageAvgPrefix = "MEMUsageAvg."
	thresholdSection  = "thresholds"
)

/*
   actions: "enqueue, allocate, backfill"
   tiers:
   - plugins:
     - name: usage
       arguments:
         usage.weight: 10
         type: average
         thresholds:
           cpu: 70
           mem: 70
         period: 10m
*/

const AVG string = "average"
const COMMON string = "common"
const MAX string = "max"

type PeriodThreshold struct {
	Period    string
	Threshold float64
}
type thresholdConfig struct {
	CpuUsageAvg PeriodThreshold
	MemUsageAvg PeriodThreshold
}
type usagePlugin struct {
	pluginArguments framework.Arguments
	weight          int
	usageType       string
	cpuThresholds   float64
	memThresholds   float64
	SamplePeriods   string
}

// New function returns usagePlugin object
func New(args framework.Arguments) framework.Plugin {
	var plugin *usagePlugin = &usagePlugin{
		pluginArguments: args,
		weight:          1,
		usageType:       AVG,
		cpuThresholds:   80,
		memThresholds:   80,
		SamplePeriods:   "10m",
	}
	args.GetInt(&plugin.weight, "usage.weight")

	if averageStr, ok := args["type"]; ok {
		if average, success := averageStr.(string); success {
			plugin.usageType = average
		} else {
			klog.Warningf("usage parameter[%v] is wrong", args)
		}
	}

	if samplePeriodsStr, ok := args["period"]; ok {
		if samplePeriods, success := samplePeriodsStr.(string); success {
			plugin.SamplePeriods = samplePeriods
		} else {
			klog.Warningf("usage parameter[%v] is wrong", args)
		}
	}
	return plugin
}

func (up *usagePlugin) Name() string {
	return PluginName
}

func (up *usagePlugin) OnSessionOpen(ssn *framework.Session) {
	klog.V(5).Infof("Enter usage plugin ...")
	defer func() {
		klog.V(5).Infof("Leaving usage plugin ...")
	}()

	if klog.V(4).Enabled() {
		for node := range ssn.Nodes {
			usage := ssn.Nodes[node].ResourceUsage
			klog.V(4).Infof("node:%v, cpu usage:%v, mem usage:%v", node, usage.CPUUsageAvg, usage.MEMUsageAvg)
		}
	}
	argsValue, ok := up.pluginArguments[thresholdSection]
	if ok {
		args, ok := argsValue.(map[interface{}]interface{})
		if !ok {
			klog.V(4).Infof("pluginArguments[thresholdsSection]:%v", argsValue)
		}
		for k, v := range args {
			key, _ := k.(string)
			value, _ := v.(int)
			switch key {
			case "cpu":
				up.cpuThresholds = float64(value)
			case "mem":
				up.memThresholds = float64(value)
			}
		}

		// here is to deal with the case that restart volcano-scheduler and there is no ResourceUsage in cache of volcano-scheduler
		if source.Period == "" {
			source.Period = up.SamplePeriods
			ssn.GetCache().GetMetricsData()
			if schedulerCache, ok := ssn.GetCache().(*cache.SchedulerCache); ok {
				for index, node := range ssn.NodeList {
					ssn.NodeList[index].ResourceUsage = schedulerCache.Nodes[node.Name].ResourceUsage
				}
			} else {
				klog.Error("chane cache to schedulerCache error")
			}
		}
	} else {
		klog.V(4).Infof("Threshold arguments :%v", argsValue)
	}

	predicateFn := func(task *api.TaskInfo, node *api.NodeInfo) error {
		if up.SamplePeriods != "" {
			klog.V(4).Infof("predicateFn cpuUsageAvg:%v,predicateFn memUsageAvg:%v", up.cpuThresholds, up.memThresholds)
			if node.ResourceUsage.CPUUsageAvg[up.SamplePeriods] > up.cpuThresholds {
				msg := fmt.Sprintf("Node %s cpu usage %f exceeds the threshold %f", node.Name, node.ResourceUsage.CPUUsageAvg[up.SamplePeriods], up.cpuThresholds)
				return fmt.Errorf("plugin %s predicates failed %s", up.Name(), msg)
			}
			if node.ResourceUsage.MEMUsageAvg[up.SamplePeriods] > up.memThresholds {
				msg := fmt.Sprintf("Node %s mem usage %f exceeds the threshold %f", node.Name, node.ResourceUsage.MEMUsageAvg[up.SamplePeriods], up.memThresholds)
				return fmt.Errorf("plugin %s memory usage predicates failed %s", up.Name(), msg)
			}
		}

		klog.V(4).Infof("Usage plugin filter for task %s/%s on node %s pass.", task.Namespace, task.Name, node.Name)
		return nil
	}

	nodeOrderFn := func(task *api.TaskInfo, node *api.NodeInfo) (float64, error) {
		score := 0.0
		if up.SamplePeriods == "" {
			return 0, nil
		}
		cpuUsage, exist := node.ResourceUsage.CPUUsageAvg[up.SamplePeriods]
		klog.V(4).Infof("Node %s cpu usage is %f.", node.Name, cpuUsage)
		if !exist {
			return 0, nil
		}
		score = (100 - cpuUsage) / 100
		score *= float64(k8sFramework.MaxNodeScore * int64(up.weight))
		klog.V(4).Infof("Node %s score for task %s is %f.", node.Name, task.Name, score)
		return score, nil
	}

	ssn.AddPredicateFn(up.Name(), predicateFn)
	ssn.AddNodeOrderFn(up.Name(), nodeOrderFn)
}

func (up *usagePlugin) OnSessionClose(ssn *framework.Session) {}
