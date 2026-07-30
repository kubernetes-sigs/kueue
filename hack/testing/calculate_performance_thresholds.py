#!/usr/bin/env python3
"""Performance Threshold Calculator - Simplified for Kueue"""

import argparse
import math
import os
import yaml
from pathlib import Path
from statistics import mean, stdev

def load_summary_files(artifacts_dir):
    """Load all summary.yaml files"""
    artifacts_dir = Path(artifacts_dir)
    cluster_queue_data = {}
    workload_class_data = {}
    
    summary_files = list(artifacts_dir.glob("**/summary.yaml"))
    
    if not summary_files:
        print(f"❌ No summary.yaml files found in {artifacts_dir}")
        return None, None
    
    print(f"Found {len(summary_files)} test result file(s)")
    
    for summary_file in sorted(summary_files):
        try:
            with open(summary_file, 'r') as f:
                data = yaml.safe_load(f)
                
            # Extract cluster queue metrics
            if 'clusterQueueClasses' in data:
                for cq_name, cq_data in data['clusterQueueClasses'].items():
                    if 'cpuAverageUsage' in cq_data and 'nominalQuota' in cq_data:
                        if cq_data['nominalQuota'] > 0:
                            usage = (cq_data['cpuAverageUsage'] * 100) / cq_data['nominalQuota']
                            if cq_name not in cluster_queue_data:
                                cluster_queue_data[cq_name] = []
                            cluster_queue_data[cq_name].append(usage)
            
            # Extract workload class metrics
            if 'workloadClasses' in data:
                for wl_class, wl_data in data['workloadClasses'].items():
                    if 'averageTimeToAdmissionMs' in wl_data:
                        if wl_class not in workload_class_data:
                            workload_class_data[wl_class] = []
                        workload_class_data[wl_class].append(float(wl_data['averageTimeToAdmissionMs']))
            
            print(f"  ✓ Loaded {summary_file}")
        except Exception as e:
            print(f"  ✗ Error loading {summary_file}: {e}")
    
    return cluster_queue_data, workload_class_data


def calculate_thresholds(cluster_queue_data, workload_class_data):
    """Calculate thresholds"""
    thresholds = {
        'cmd': {
            'maxWallMs': 500000
        },
        'clusterQueueClassesMinUsage': {},
        'wlClassesMaxAvgTimeToAdmissionMs': {}
    }
    
    # Calculate cluster queue thresholds (use mean)
    print("\n📊 CLUSTER QUEUE UTILIZATION")
    for cq_name, values in cluster_queue_data.items():
        if values:
            m = mean(values)
            threshold = m * 0.80  # -10% (more conservative)
            thresholds['clusterQueueClassesMinUsage'][cq_name] = round(threshold, 1)
            print(f"  {cq_name}: mean={m:.1f}% → threshold={threshold:.1f}%")
    
    # Calculate workload class thresholds (mean + 20%)
    print("\n📊 WORKLOAD ADMISSION TIME")
    for wl_class, values in workload_class_data.items():
        if values:
            m = mean(values)
            threshold = m * 1.20  # +20%
            threshold = int(math.ceil(threshold / 1000) * 1000)  # Round to 1000ms
            thresholds['wlClassesMaxAvgTimeToAdmissionMs'][wl_class] = threshold
            print(f"  {wl_class}: mean={m:.0f}ms → threshold={threshold}ms")
    
    return thresholds


def generate_yaml(thresholds):
    """Generate YAML content"""
    yaml_content = """# Performance Test - Expected Performance Ranges
#
# Thresholds are automatically calculated based on historical test results
# using statistical analysis (mean + margin).
#

# Command execution limits
# Threshold: 500000ms (default)
cmd:
  maxWallMs: 500000

# Cluster Queue utilization targets (minimum)
# Threshold: mean, rounded to 0.1%
clusterQueueClassesMinUsage:
"""
    
    for cq_name, threshold in sorted(thresholds['clusterQueueClassesMinUsage'].items()):
        yaml_content += f"  {cq_name}: {threshold}\n"
    
    yaml_content += """
# Workload admission time limits
# Threshold: mean + 20% rounded up to nearest 1000ms
wlClassesMaxAvgTimeToAdmissionMs:
"""
    
    for wl_class, threshold in sorted(thresholds['wlClassesMaxAvgTimeToAdmissionMs'].items()):
        yaml_content += f"  {wl_class}: {threshold}\n"
    
    return yaml_content


def main():
    parser = argparse.ArgumentParser(description='Calculate performance thresholds')
    parser.add_argument('--artifacts-dir', default='./artifacts', help='Artifacts directory')
    parser.add_argument('--dry-run', action='store_true', help='Preview only')
    args = parser.parse_args()
    
    print(f"🔍 Loading test results from: {args.artifacts_dir}")
    
    # Load data
    cluster_queue_data, workload_class_data = load_summary_files(args.artifacts_dir)
    
    if not cluster_queue_data and not workload_class_data:
        print("❌ No metrics found")
        return 1
    
    print(f"✓ Loaded {len(list(cluster_queue_data.keys()) + list(workload_class_data.keys()))} metrics")
    
    # Calculate thresholds
    thresholds = calculate_thresholds(cluster_queue_data, workload_class_data)
    
    # Generate YAML
    yaml_content = generate_yaml(thresholds)
    
    if args.dry_run:
        print("\n📋 DRY RUN - No files modified\n")
        print(yaml_content)
    else:
        # Write to file
        output_file = Path('./test/performance/scheduler/configs/baseline/rangespec.yaml')
        output_file.parent.mkdir(parents=True, exist_ok=True)
        
        with open(output_file, 'w') as f:
            f.write(yaml_content)
        
        print(f"\n✅ Updated: {output_file}")
    
    return 0


if __name__ == '__main__':
    exit(main())