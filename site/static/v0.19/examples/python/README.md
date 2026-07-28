# Kueue in Python

Documentation for these examples can be found [on the Kueue documentation site](https://kueue.sigs.k8s.io/docs/tasks/run/python_jobs/).

## Running

Each script includes [inline script metadata (PEP 723)](https://peps.python.org/pep-0723/)
so all dependencies are installed automatically. Just run any script with
[uv](https://docs.astral.sh/uv/):

```bash
uv run sample-job.py
```

No virtual environment or `pip install` step is required.
