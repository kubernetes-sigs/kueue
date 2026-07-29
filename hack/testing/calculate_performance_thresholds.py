#!/usr/bin/env python3
"""
Performance Threshold Calculator for Kueue

Analyzes historical performance test runs and automatically calculates
appropriate thresholds using statistical analysis (mean + N standard deviations).

Usage:
    python3 calculate_performance_thresholds.py --artifacts-dir /path/to/artifacts --output rangespec.yaml
    python3 calculate_performance_thresholds.py --help
"""

import argparse
import json
import math
import os
import sys
import yaml
from pathlib import Path
from dataclasses import dataclass, asdict
from typing import Dict, List, Tuple, Optional
from statistics import mean, stdev, StatisticsError

@dataclass
class ThresholdConfig:
    """Configuration for threshold calculation"""
    percentile_stddev: float = 6.46  # For 1 in a million (6.46 sigma)
    cmd_margin: float = 0.20  # 20% margin for command execution time
    utilization_margin: float = -0.07  # -7% margin for minimum utilization
    admission_time_margin: float = 0.20  # 20% margin for admission times
    min_runs_required: int = 5  # Minimum historical runs needed
    rounding_interval: int = 1000  # Round to nearest 1000ms

@dataclass
class MetricStats:
    """Statistics for a metric across multiple runs"""
    metric_name: str
    values: List[float]
    mean: float = 0.0
    stddev: float = 0.0
    min_val: float = 0.0
    max_val: float = 0.0
    percentile_99: float = 0.0
    
    def calculate(self):
        """Calculate statistics from values"""
        if not self.values:
            return
        
        self.mean = mean(self.values)
        self.min_val = min(self.values)
        self.max_val = max(self.values)
        
        if len(self.values) > 1:
            try:
                self.stddev = stdev(self.values)
                # Calculate 99th percentile (mean + 2.326 sigma)
                self.percentile_99 = self.mean + (2.326 * self.stddev)
            except StatisticsError:
                self.stddev = 0.0
                self.percentile_99 = self.mean


class PerformanceThresholdCalculator:
    """Analyzes performance test results and calculates thresholds"""
    
    def __init__(self, config: ThresholdConfig):
        self.config = config
        self.cmd_metrics: MetricStats = MetricStats("cmd.maxWallMs", [])
        self.cluster_queue_metrics: Dict[str, MetricStats] = {}
        self.workload_class_metrics: Dict[str, MetricStats] = {}
    
    def load_test_results(self, artifacts_dir: Path) -> int:
        """
        Load test results from artifacts directory.
        
        Looks for summary.yaml files in run directories:
        - artifacts/run-performance-scheduler/summary.yaml
        - artifacts/run-tas-performance-scheduler/summary.yaml
        
        Returns number of runs found
        """
        run_count = 0
        artifacts_dir = Path(artifacts_dir)
        
        # Patterns to search for
        patterns = [
            "run-performance-scheduler/summary.yaml",
            "run-performance-scheduler-*/summary.yaml",
            "run-tas-performance-scheduler/summary.yaml",
            "run-tas-performance-scheduler-*/summary.yaml",
        ]
        
        summary_files = []
        for pattern in patterns:
            summary_files.extend(artifacts_dir.glob(pattern))
        
        if not summary_files:
            print(f"Warning: No summary.yaml files found in {artifacts_dir}")
            print(f"Searched patterns: {patterns}")
            return 0
        
        print(f"Found {len(summary_files)} test result file(s)")
        
        for summary_file in sorted(summary_files):
            try:
                with open(summary_file, 'r') as f:
                    data = yaml.safe_load(f)
                    if data:
                        self._extract_metrics(data)
                        run_count += 1
                        print(f"  ✓ Loaded {summary_file}")
            except Exception as e:
                print(f"  ✗ Error loading {summary_file}: {e}")
        
        return run_count
    
    def _extract_metrics(self, data: dict):
        """Extract metrics from a single test result"""
        try:
            # Extract command wall time
            if 'flat' in data and 'wallMs' in data['flat']:
                self.cmd_metrics.values.append(float(data['flat']['wallMs']))
            
            # Extract cluster queue metrics (minimum usage percentages)
            if 'clusterQueueClasses' in data:
                for cq_name, cq_data in data['clusterQueueClasses'].items():
                    if 'cpuAverageUsage' in cq_data and 'nominalQuota' in cq_data:
                        if cq_data['nominalQuota'] > 0:
                            usage = (cq_data['cpuAverageUsage'] * 100) / cq_data['nominalQuota']
                            if cq_name not in self.cluster_queue_metrics:
                                self.cluster_queue_metrics[cq_name] = MetricStats(f"cq.{cq_name}", [])
                            self.cluster_queue_metrics[cq_name].values.append(usage)
            
            # Extract workload class admission time metrics
            if 'workloadClasses' in data:
                for wl_class, wl_data in data['workloadClasses'].items():
                    if 'averageTimeToAdmissionMs' in wl_data:
                        if wl_class not in self.workload_class_metrics:
                            self.workload_class_metrics[wl_class] = MetricStats(f"wl.{wl_class}", [])
                        self.workload_class_metrics[wl_class].values.append(
                            float(wl_data['averageTimeToAdmissionMs'])
                        )
        except Exception as e:
            print(f"  Warning: Error extracting metrics: {e}")
    
    def calculate_thresholds(self) -> dict:
        """Calculate thresholds based on collected metrics"""
        if not self.cmd_metrics.values:
            raise ValueError("No metrics collected. Check artifacts directory and file format.")
        
        if len(self.cmd_metrics.values) < self.config.min_runs_required:
            print(f"Warning: Only {len(self.cmd_metrics.values)} runs found, "
                  f"recommend at least {self.config.min_runs_required}")
        
        # Calculate statistics
        self.cmd_metrics.calculate()
        for metrics in self.cluster_queue_metrics.values():
            metrics.calculate()
        for metrics in self.workload_class_metrics.values():
            metrics.calculate()
        
        # Generate thresholds
        thresholds = {
            'cmd': {
                'maxWallMs': self._calculate_cmd_threshold()
            },
            'clusterQueueClassesMinUsage': {},
            'wlClassesMaxAvgTimeToAdmissionMs': {}
        }
        
        # Calculate cluster queue thresholds
        for cq_name, metrics in sorted(self.cluster_queue_metrics.items()):
            threshold = self._calculate_utilization_threshold(metrics)
            thresholds['clusterQueueClassesMinUsage'][cq_name] = threshold
        
        # Calculate workload class thresholds
        for wl_class, metrics in sorted(self.workload_class_metrics.items()):
            threshold = self._calculate_admission_time_threshold(metrics)
            thresholds['wlClassesMaxAvgTimeToAdmissionMs'][wl_class] = threshold
        
        return thresholds
    
    def _calculate_cmd_threshold(self) -> int:
        """Calculate command execution time threshold (mean + margin)"""
        threshold = self.cmd_metrics.mean * (1 + self.config.cmd_margin)
        return self._round_threshold(threshold)
    
    def _calculate_utilization_threshold(self, metrics: MetricStats) -> float:
        """Calculate cluster queue utilization threshold (mean - margin)"""
        threshold = metrics.mean * (1 + self.config.utilization_margin)
        return round(threshold, 1)
    
    def _calculate_admission_time_threshold(self, metrics: MetricStats) -> int:
        """Calculate workload admission time threshold (mean + margin)"""
        threshold = metrics.mean * (1 + self.config.admission_time_margin)
        return self._round_threshold(threshold)
    
    def _round_threshold(self, value: float) -> int:
        """Round threshold to nearest interval"""
        return int(math.ceil(value / self.config.rounding_interval) * self.config.rounding_interval)
    
    def print_analysis(self):
        """Print detailed analysis of collected metrics"""
        print("\n" + "="*80)
        print("PERFORMANCE METRICS ANALYSIS")
        print("="*80)
        
        print("\n📊 COMMAND EXECUTION TIME (maxWallMs)")
        print("-" * 80)
        self._print_metric_stats(self.cmd_metrics)
        
        if self.cluster_queue_metrics:
            print("\n📊 CLUSTER QUEUE UTILIZATION (minUsage %)")
            print("-" * 80)
            for cq_name, metrics in sorted(self.cluster_queue_metrics.items()):
                print(f"\n  {cq_name}:")
                self._print_metric_stats(metrics)
        
        if self.workload_class_metrics:
            print("\n📊 WORKLOAD CLASS ADMISSION TIME (maxAvgTimeToAdmissionMs)")
            print("-" * 80)
            for wl_class, metrics in sorted(self.workload_class_metrics.items()):
                print(f"\n  {wl_class}:")
                self._print_metric_stats(metrics)
    
    def _print_metric_stats(self, metrics: MetricStats):
        """Print statistics for a metric"""
        if not metrics.values:
            print("  No data")
            return
        
        cv = (metrics.stddev / metrics.mean * 100) if metrics.mean > 0 else 0
        print(f"  Runs:      {len(metrics.values)}")
        print(f"  Mean:      {metrics.mean:,.0f}")
        print(f"  StdDev:    {metrics.stddev:,.0f}")
        print(f"  Min:       {metrics.min_val:,.0f}")
        print(f"  Max:       {metrics.max_val:,.0f}")
        print(f"  CV%:       {cv:.2f}%")


def load_existing_rangespec(rangespec_path: Path) -> dict:
    """Load existing rangespec.yaml to preserve comments and structure"""
    if rangespec_path.exists():
        with open(rangespec_path, 'r') as f:
            return yaml.safe_load(f)
    return {}


def generate_rangespec_yaml(thresholds: dict, existing: dict = None) -> str:
    """
    Generate YAML content for rangespec.yaml with comments
    """
    yaml_content = """# Performance Test - Expected Performance Ranges
#
# Thresholds are automatically calculated based on historical test results
# using statistical analysis (mean + margin).
#
# Last updated: """ + __import__('datetime').datetime.now().isoformat() + """
#

# Command execution limits
# Threshold: mean + 20% rounded up to nearest 1000ms
cmd:
  maxWallMs: """ + str(thresholds['cmd']['maxWallMs']) + """

# Cluster Queue utilization targets (minimum)
# Threshold: mean - 7%, rounded down to 0.1%
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
        yaml_content += f"  {wl_class}: {threshold:>10} \n"
    
    return yaml_content


def main():
    parser = argparse.ArgumentParser(
        description='Calculate performance test thresholds based on historical results',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Analyze results and update rangespec.yaml
  python3 calculate_performance_thresholds.py --artifacts-dir ./artifacts
  
  # Save to custom location
  python3 calculate_performance_thresholds.py --artifacts-dir ./artifacts \\
                                              --output ./test/performance/scheduler/configs/baseline/rangespec.yaml
  
  # Dry run (show analysis without writing)
  python3 calculate_performance_thresholds.py --artifacts-dir ./artifacts --dry-run
        """
    )
    
    parser.add_argument(
        '--artifacts-dir',
        type=Path,
        default=Path('./artifacts'),
        help='Directory containing test artifacts (default: ./artifacts)'
    )
    
    parser.add_argument(
        '--output',
        type=Path,
        default=None,
        help='Output file for rangespec.yaml (default: auto-detect from repo)'
    )
    
    parser.add_argument(
        '--dry-run',
        action='store_true',
        help='Show analysis and new thresholds without writing files'
    )
    
    parser.add_argument(
        '--cmd-margin',
        type=float,
        default=0.20,
        help='Margin for command execution time (default: 0.20 = 20%%)'
    )
    
    parser.add_argument(
        '--util-margin',
        type=float,
        default=-0.07,
        help='Margin for cluster queue utilization (default: -0.07 = -7%%)'
    )
    
    parser.add_argument(
        '--admission-margin',
        type=float,
        default=0.20,
        help='Margin for admission time (default: 0.20 = 20%%)'
    )
    
    parser.add_argument(
        '--min-runs',
        type=int,
        default=5,
        help='Minimum historical runs required (default: 5)'
    )
    
    args = parser.parse_args()
    
    # Validate artifacts directory
    if not args.artifacts_dir.exists():
        print(f"❌ Error: Artifacts directory not found: {args.artifacts_dir}")
        sys.exit(0)
    
    # Determine output path
    if args.output is None:
        # Try to find rangespec.yaml in repo
        possible_paths = [
            Path('./test/performance/scheduler/configs/baseline/rangespec.yaml'),
            Path('./test/performance/scheduler/configs/tas/rangespec.yaml'),
        ]
        for path in possible_paths:
            if path.exists():
                args.output = path
                break
        
        if args.output is None:
            args.output = Path('./test/performance/scheduler/configs/baseline/rangespec.yaml')
    
    print(f"\n🔍 Loading test results from: {args.artifacts_dir}")
    
    # Create calculator with config
    config = ThresholdConfig(
        cmd_margin=args.cmd_margin,
        utilization_margin=args.util_margin,
        admission_time_margin=args.admission_margin,
        min_runs_required=args.min_runs
    )
    
    calculator = PerformanceThresholdCalculator(config)
    
    # Load results
    run_count = calculator.load_test_results(args.artifacts_dir)
    
    if run_count == 0:
        print("❌ Error: No test results found")
        sys.exit(1)
    
    print(f"✓ Loaded {run_count} test run(s)\n")
    
    # Calculate thresholds
    try:
        thresholds = calculator.calculate_thresholds()
    except ValueError as e:
        print(f"❌ Error calculating thresholds: {e}")
        sys.exit(1)
    
    # Print analysis
    calculator.print_analysis()
    
    # Print new thresholds
    print("\n" + "="*80)
    print("CALCULATED THRESHOLDS")
    print("="*80)
    yaml_content = generate_rangespec_yaml(thresholds)
    print(yaml_content)
    
    # Write output if not dry-run
    if args.dry_run:
        print("\n📋 DRY RUN MODE - No files written")
        print(f"   To apply changes, run without --dry-run")
    else:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        
        # Backup existing file
        if args.output.exists():
            backup_path = args.output.with_suffix('.yaml.bak')
            import shutil
            shutil.copy2(args.output, backup_path)
            print(f"\n💾 Backed up existing file to: {backup_path}")
        
        # Write new file
        with open(args.output, 'w') as f:
            f.write(yaml_content)
        
        print(f"\n✅ Successfully wrote thresholds to: {args.output}")
        print(f"\n📝 Next steps:")
        print(f"   1. Review the changes: git diff {args.output}")
        print(f"   2. Commit the changes: git add {args.output}")
        print(f"   3. Push and create a PR")


if __name__ == '__main__':
    main()
