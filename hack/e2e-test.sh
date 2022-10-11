export KUSTOMIZE=$PWD/bin/kustomize
export GINKGO=$PWD/bin/ginkgo
export KIND=$PWD/bin/kind
function cleanup {
    if [ $USE_EXISTING_CLUSTER == 'false' ] 
    then 
        $KIND delete cluster --name $KIND_CLUSTER_NAME
    fi
    (cd config/components/manager && $KUSTOMIZE edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)
}
function startup {
    if [ $USE_EXISTING_CLUSTER == 'false' ] 
    then 
        $KIND create cluster --name $KIND_CLUSTER_NAME --image $E2E_KIND_VERSION
    fi
}
function kind_load {
    $KIND load docker-image $IMAGE_TAG --name $KIND_CLUSTER_NAME
}
function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)
    kubectl apply -k test/e2e/config
}
trap cleanup EXIT
startup
kind_load
kueue_deploy
$GINKGO --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/e2e/...

