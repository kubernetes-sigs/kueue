#!/bin/sh

fluxuser=$(whoami)
fluxuid=$(id -u $fluxuser)

# Ensure spack view is on the path, wherever it is mounted
viewbase="/mnt/flux"
viewroot=${viewbase}/view
configroot=${viewbase}/config
software="${viewbase}/software"
viewbin="${viewroot}/bin"
fluxpath=${viewbin}/flux

# Important to add AFTER in case software in container duplicated (e.g., Python)
export PATH=$PATH:${viewbin}

# Copy mount software to /opt/software
cp -R ${viewbase}/software/* /opt/software/

# Flux should use the Python with its install
foundroot=$(find $viewroot -maxdepth 2 -type d -path $viewroot/lib/python3*) > /dev/null 2>&1
pythonversion=$(basename ${foundroot})
pythonversion=${viewroot}/bin/${pythonversion}
echo "Python version: $pythonversion"
echo "Python root: $foundroot"

# If we found the right python, ensure it's linked (old link does not work)
if [[ -f "${pythonversion}" ]]; then
   rm -rf $viewroot/bin/python3
   rm -rf $viewroot/bin/python
   ln -s ${pythonversion} $viewroot/lib/python  || true
   ln -s ${pythonversion} $viewroot/lib/python3 || true
fi

# Ensure we have flux's python on the path
export PYTHONPATH=${PYTHONPATH:-""}:${foundroot}/site-packages
export FLUX_RC_EXTRA=$viewroot/etc/flux/rc1.d

# Write a script to load fluxion
cat <<EOT >> /tmp/load-fluxion.sh
flux module remove sched-simple
flux module load sched-fluxion-resource
flux module load sched-fluxion-qmanager
EOT
mv /tmp/load-fluxion.sh ${viewbase}/load-fluxion.sh

# Write an easy file we can source for the environment
cat <<EOT >> /tmp/flux-view.sh
#!/bin/bash
export PATH=$PATH
export PYTHONPATH=$PYTHONPATH
export LD_LIBRARY_PATH=${LD_LIBRARY_PATH:-""}:$viewroot/lib
export fluxsocket=local://${configroot}/run/flux/local
EOT
mv /tmp/flux-view.sh ${viewbase}/flux-view.sh

# Variables we can use again
cfg="${configroot}/etc/flux/config"
command="$@"

# Copy mounted curve to expected location
curvepath=/mnt/flux/config/etc/curve/curve.cert
cp /curve/curve.cert ${curvepath}

# Remove group and other read
chmod o-r ${curvepath}
chmod g-r ${curvepath}
chown -R ${fluxuid} ${curvepath}

# Generate host resources
hosts=$(cat ${configroot}/etc/flux/system/hostlist)
flux R encode --hosts=${hosts} --local > /tmp/R
mv /tmp/R ${configroot}/etc/flux/system/R

# Put the state directory in /var/lib on shared view
export STATE_DIR=${configroot}/var/lib/flux
export FLUX_OUTPUT_DIR=/tmp/fluxout
mkdir -p ${STATE_DIR} ${FLUX_OUTPUT_DIR}

# Main host <name>-0 and the fully qualified domain name
mainHost="%s"
workdir=$(pwd)

# Make cron.d directory
mkdir -p ${configroot}/etc/flux/system/cron.d
brokerOptions="-Scron.directory=${configroot}/etc/flux/system/cron.d \
  -Stbon.fanout=256 \
  -Srundir=${configroot}/run/flux  \
  -Sstatedir=${STATE_DIR} -Slocal-uri=local://$configroot/run/flux/local \
  -Slog-stderr-level=0  \
  -Slog-stderr-mode=local"

# Run an interactive cluster, giving no command to flux start
run_interactive_cluster() {
    echo "🌀 flux broker --config-path ${cfg} ${brokerOptions}"
    flux broker --config-path ${cfg} ${brokerOptions}
}

# Start flux with the original entrypoint
if [ "$(hostname)" = "${mainHost}" ]; then

  echo "Command provided is: ${command}"
  if [ -z "${command}" ]; then
    run_interactive_cluster
  else

    # If tasks are == 0, then only define nodes
    flags="%s  "
    echo "Flags for flux are ${flags}"
    echo "🌀 flux start  -o --config ${cfg} ${brokerOptions} flux submit ${flags} --quiet --watch ${command}"
	flux start  -o --config ${cfg} ${brokerOptions} flux run ${flags} ${command}
  fi

# Block run by workers
else

    # We basically sleep/wait until the lead broker is ready
    echo "🌀 flux start  -o --config ${configroot}/etc/flux/config ${brokerOptions}"

    # We can keep trying forever, don't care if worker is successful or not
    # Unless retry count is set, in which case we stop after retries
    while true
    do
        flux start -o --config ${configroot}/etc/flux/config ${brokerOptions}
        retval=$?
        if [[ "${retval}" -eq 0 ]] || [[ "false" == "true" ]]; then
             echo "The follower worker exited cleanly. Goodbye!"
             break
        fi
        echo "Return value for follower worker is ${retval}"
        echo "😪 Sleeping 15s to try again..."
        sleep 15
    done
fi

# Marker of completion, if needed
touch $viewbase/flux-operator-complete.txt
