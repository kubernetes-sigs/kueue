#!/bin/bash

#SBATCH --array=1-3%2

echo "now processing task id:: ${SLURM_ARRAY_TASK_ID}"
python /home/slurm/samples/sample_code.py

exit 0
