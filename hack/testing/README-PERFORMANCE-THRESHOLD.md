# Performance Threshold Automation

Automated calculation of performance test thresholds based on historical test results.

## Quick Start

```bash
# Collect performance test data (5-10 runs recommended)
make update-performance-thresholds collect

# Preview threshold changes (dry-run, no files modified)
make update-performance-thresholds calculate dry

# Apply threshold changes to rangespec.yaml
make update-performance-thresholds calculate

# Validate metrics exist in artifacts
make update-performance-thresholds validate
```

## How It Works

1. **Collects** execution times from multiple performance test runs
2. **Analyzes** metrics using statistical methods (mean, standard deviation)
3. **Calculates** thresholds using formula: `mean + margin`
   - Command time: mean + 20%
   - Cluster queue utilization: mean - 7%
   - Workload admission time: mean + 20%
4. **Updates** `test/performance/scheduler/configs/baseline/rangespec.yaml` automatically
5. **Creates** automatic backup of old file

## Files

- `performance-thresholds.sh` - Main bash automation script
- `calculate_performance_thresholds.py` - Python threshold calculation logic

## Supported Metrics

### Command Execution Time
- Metric: `cmd.maxWallMs`
- Source: `summary.yaml` → `flat.wallMs`
- Calculation: mean + 20%, rounded to nearest 1000ms

### Cluster Queue Utilization
- Metric: `clusterQueueClassesMinUsage`
- Source: `summary.yaml` → `clusterQueueClasses.*.cpuAverageUsage`
- Calculation: mean - 7%, minimum threshold

### Workload Admission Time
- Metric: `wlClassesMaxAvgTimeToAdmissionMs`
- Source: `summary.yaml` → `workloadClasses.*.averageTimeToAdmissionMs`
- Calculation: mean + 20%, rounded to nearest 1000ms

## Requirements

- Python 3.7+
- PyYAML (automatically installed if missing)
- Git
- Bash/Zsh shell

## Troubleshooting

**No artifacts found:**
```bash
make run-performance-scheduler
make update-performance-thresholds calculate dry
```

**PyYAML not installed:**
```bash
pip install PyYAML
```

**Want to see usage:**
```bash
make update-performance-thresholds help
```

## Related Issues

- #12841 - Automated script to set performance threshold based on historical results and stat analysis
