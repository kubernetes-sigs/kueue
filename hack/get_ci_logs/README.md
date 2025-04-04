The script is designed to recursively download files and directories from a specified HTTP URL, saving them locally while maintaining the original directory structure. It is particularly useful for downloading artifacts of CI/CD, build logs.

Before running the script, ensure you have Python installed on your system. The script is compatible with Python 3.6 and above.

Create and activate a Python virtual environment to isolate dependencies and avoid conflicts with system-wide packages.
```
Create your venv:
VENV_DIR=<path_to_your_venv_workspace>
python3 -m venv $VENV_DIR

Activate the venv.
source $VENV_DIR/bin/activate
```

Run the following command in your terminal to install the required packages:
```
pip install -r requirements.txt
```

To use the script, run it from the command line with the following syntax:
```
python get_ci_logs.py <URL>
```
example:
```
python get_ci_logs.py https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/4568/pull-kueue-test-integration-multikueue-main/1900509723589873664/
```
