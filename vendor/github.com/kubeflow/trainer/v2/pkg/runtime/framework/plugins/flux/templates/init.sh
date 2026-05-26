#!/bin/sh
fluxroot=%s
mainHost=%s

# We need to "install" config assets separately. We may not have write to /opt/view.
installRoot=/mnt/flux/config
echo "Hello I am hostname $(hostname) running setup."

# Always use verbose, no reason to not here
echo "Flux install root: ${fluxroot}"
export fluxroot

# Add flux to the path (if using view)
export PATH=/opt/view/bin:$PATH

# If the view doesn't exist, ensure basic paths do
mkdir -p $fluxroot/bin

# Cron directory
mkdir -p $installRoot/etc/flux/system/cron.d
mkdir -p $installRoot/var/lib/flux

# These actions need to happen on all hosts
mkdir -p $installRoot/etc/flux/system
hosts="%s"

# Echo hosts here in case the main container needs to generate
echo "${hosts}" > ${installRoot}/etc/flux/system/hostlist

# Write the broker configuration
mkdir -p ${installRoot}/etc/flux/config
cat <<EOT >> ${installRoot}/etc/flux/config/broker.toml
%s
EOT

echo
echo "🐸 Broker Configuration"
cat ${installRoot}/etc/flux/config/broker.toml

# The rundir needs to be created first, and owned by user flux
# Along with the state directory and curve certificate
mkdir -p ${installRoot}/run/flux ${installRoot}/etc/curve

viewroot=/mnt/flux
mkdir -p $viewroot/view

# Now prepare to copy finished spack view over
echo "Moving content from /opt/view to be in shared volume at $viewroot"
# Note that /opt/view is a symlink to here
view=$(ls /opt/views/._view/)
view="/opt/views/._view/${view}"

# We have to move both of these paths - spack makes link to /opt/software
# /opt/software will need to be restored in application container
cp -R ${view}/* $viewroot/view
cp -R /opt/software $viewroot/

# This is a marker to indicate the copy is done
touch $viewroot/flux-operator-done.txt
echo "Application is done."
