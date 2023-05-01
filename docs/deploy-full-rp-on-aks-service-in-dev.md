# Deploy an Entire RP on AKS Development Service

## Prerequisites

1. Your development environment is prepared according to the steps outlined in [Prepare Your Dev Environment](./prepare-your-dev-environment.md)

1. Your Azure subscription is running a shared ARO-RP development environment according to the steps in [Prepare a shared RP development environment](./prepare-a-shared-rp-development-environment.md)

1. Code changes in https://github.com/cadenmarchese/ARO-RP/tree/arorp-aks-poc1 are checked out locally. This branch is work-in-progress, and contains commits to allow RP to run as a pod, and accept requests from inside an AKS cluster.


## Deploying an int-like Development RP in AKS

1. Fetch the most up-to-date secrets with `make secrets`

1. Copy and source your environment file.

    ```bash
    cp env.example env
    vi env
    . ./env
    ```

1. Create a full environment file, which overrides some default `./env` options when sourced

    ```bash
    cp env-int.example env-int
    vi env-int
    . ./env-int
    ```

1. Generate the development RP configuration

    ```bash
    make dev-config.yaml
    ```

1. Run `make deploy`. This will fail on the first attempt to run due to AKS not being installed, so after the first failure, please skip to the next step to deploy the VPN Gateway and then deploy AKS.
    > __NOTE:__ If the deployment fails with `InvalidResourceReference` due to the RP Network Security Groups not found, delete the "gateway-production-predeploy" deployment in the gateway resource group, and re-run `make deploy`.

    > __NOTE:__ If the deployment fails with `A vault with the same name already exists in deleted state`, then you will need to recover the deleted keyvaults from a previous deploy using: `az keyvault recover --name <KEYVAULT_NAME>` for each keyvault, and re-run.

1. Deploy a VPN Gateway. This is required in order to be able to connect to AKS from your local machine:

    ```bash
    source ./hack/devtools/deploy-shared-env.sh
    deploy_vpn_for_dedicated_rp
    ```

1. Deploy AKS by running these commands from the ARO-RP root directory:

    ```bash
    source ./hack/devtools/deploy-shared-env.sh
    deploy_aks_dev
    ```
    > __NOTE:__ If the AKS deployment fails with missing RP VNETs, delete the "gateway-production-predeploy" deployment in the gateway resource group, and re-run `make deploy` and then re-run `deploy_aks_dev`.

1. Download the VPN config. Please note that this action will _**OVER WRITE**_ the `secrets/vpn-$LOCATION.ovpn` on your local machine. **DO NOT** run `make secrets-update` after doing this, as you will overwrite existing config, until such time as you have run `make secrets` to get the config restored.

    ```bash
    vpn_configuration
    ```

1. Connect to the Dev VPN in a new terminal:

    ```bash
    sudo openvpn secrets/vpn-$LOCATION.ovpn
    ```

1. Now that your machine is able access the VPN, you can log into the AKS cluster:

    ```bash
    make aks.kubeconfig
    ./hack/hive-generate-config.sh
    export KUBECONFIG=$(pwd)/aks.kubeconfig
    kubectl get nodes
    ```

    > __NOTE:__ There are multiple AKS clusters running in our dev sub with the same name. You can confirm that you are connected to the correct cluster by running `kubectl cluster-info` and confirming that the cluster hostname matches the hostname of the desired AKS cluster.

1. Mirror the OpenShift and ARO images to your new ACR
    <!-- TODO (bv) allow mirroring through a pipeline would be faster and a nice to have -->
    > __NOTE:__ Running the mirroring through a VM in Azure rather than a local workstation is recommended for better performance.

    1. Setup mirroring environment variables

        ```bash
        export DST_ACR_NAME=${USER}aro
        export SRC_AUTH_QUAY=$(echo $USER_PULL_SECRET | jq -r '.auths."quay.io".auth')
        export SRC_AUTH_REDHAT=$(echo $USER_PULL_SECRET | jq -r '.auths."registry.redhat.io".auth')
        export DST_AUTH=$(echo -n '00000000-0000-0000-0000-000000000000:'$(az acr login -n ${DST_ACR_NAME} --expose-token | jq -r .accessToken) | base64 -w0)
        ```

    1. Login to the Azure Container Registry

        ```bash
        docker login -u 00000000-0000-0000-0000-000000000000 -p "$(echo $DST_AUTH | base64 -d | cut -d':' -f2)" "${DST_ACR_NAME}.azurecr.io"
        ```

    1. Run the mirroring
        > The `latest` argument will take the DefaultInstallStream from `pkg/util/version/const.go` and mirror that version

        ```bash
        go run -tags aro ./cmd/aro mirror latest
        ```
        If you are going to test or work with multi-version installs, then you should mirror any additional versions as well, for example for 4.11.21 it would be

        ```bash
        go run -tags aro ./cmd/aro mirror 4.11.21
        ```

    1. Push the ARO and Fluentbit images to your ACR

        > If running this step from a VM separate from your workstation, ensure the commit tag used to build the image matches the commit tag where `make deploy` is run.

        > Due to security compliance requirements, `make publish-image-*` targets pull from `arointsvc.azurecr.io`. You can either authenticate to this registry using `az acr login --name arointsvc` to pull the image, or modify the $RP_IMAGE_ACR environment variable locally to point to `registry.access.redhat.com` instead.

        ```bash
        make publish-image-aro-multistage
        make publish-image-fluentbit
        ```

    1. Update `kubernetes_resources/deployments.yaml` with the correct registry, image repository, and tag created above, so that the RP deployment can pull its image.

    1. Authorize the AKS cluster to [pull images from your ACR](https://learn.microsoft.com/en-us/troubleshoot/azure/azure-kubernetes/cannot-pull-image-from-acr-to-aks-cluster)

        ```bash
        az aks update -n <myAKSCluster> -g <myResourceGroup> --attach-acr <acr-resource-id>
        ```

1. Update the DNS Child Domains

    ```bash
    export PARENT_DOMAIN_NAME=osadev.cloud
    export PARENT_DOMAIN_RESOURCEGROUP=dns
    export GLOBAL_RESOURCEGROUP=$USER-global

    for DOMAIN_NAME in $USER-clusters.$PARENT_DOMAIN_NAME $USER-rp.$PARENT_DOMAIN_NAME; do
        CHILD_DOMAIN_PREFIX="$(cut -d. -f1 <<<$DOMAIN_NAME)"
        echo "########## Creating NS record to DNS Zone $CHILD_DOMAIN_PREFIX ##########"
        az network dns record-set ns create \
            --resource-group "$PARENT_DOMAIN_RESOURCEGROUP" \
            --zone "$PARENT_DOMAIN_NAME" \
            --name "$CHILD_DOMAIN_PREFIX" >/dev/null
        for ns in $(az network dns zone show \
            --resource-group "$GLOBAL_RESOURCEGROUP" \
            --name "$DOMAIN_NAME" \
            --query nameServers -o tsv); do
            az network dns record-set ns add-record \
            --resource-group "$PARENT_DOMAIN_RESOURCEGROUP" \
            --zone "$PARENT_DOMAIN_NAME" \
            --record-set-name "$CHILD_DOMAIN_PREFIX" \
            --nsdname "$ns" >/dev/null
        done
    done
    ```

<!-- TODO: this is almost duplicated elsewhere.  Would be nice to move to common area -->
1. Update the certificates in keyvault
    > __NOTE:__ If you reuse an old name, you might run into soft-delete of the keyvaults. Run `az keyvault recover --name` to fix this.

    > __NOTE:__ Check to ensure that the $KEYVAULT_PREFIX environment variable set on workstation matches the prefix deployed into the resource group.

    ```bash
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name rp-mdm \
        --file secrets/rp-metrics-int.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-gwy" \
        --name gwy-mdm \
        --file secrets/rp-metrics-int.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name rp-mdsd \
        --file secrets/rp-logging-int.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-gwy" \
        --name gwy-mdsd \
        --file secrets/rp-logging-int.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name cluster-mdsd \
        --file secrets/cluster-logging-int.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name dev-arm \
        --file secrets/arm.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name rp-firstparty \
        --file secrets/firstparty.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-svc" \
        --name rp-server \
        --file secrets/localhost.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-por" \
        --name portal-server \
        --file secrets/localhost.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-por" \
        --name portal-client \
        --file secrets/portal-client.pem >/dev/null
    az keyvault certificate import \
        --vault-name "$KEYVAULT_PREFIX-dbt" \
        --name dbtoken-server \
        --file secrets/localhost.pem >/dev/null
    ```

1. Delete or scale down the RP VMSS created in the deploy step

    ```bash
    az vmss delete -g ${RESOURCEGROUP} --name rp-vmss-$(git rev-parse --short=7 HEAD)$([[ $(git status --porcelain) = "" ]] || echo -dirty)
    ```

1. Update `kubernetes_resources/configmaps.yaml` with your environment variables
    1. Populate `AZURE_FP_CLIENT_ID` with the `fpClientID` that was previously populated in `dev-config.yaml`.
    1. Populate `ACR_RESOURCE_ID` with the full resource ID of the ACR that you created in an earlier step.
    1. Populate `GATEWAY_RESOURCEGROUP` with the newly created gateway resource group, `KEYVAULT_PREFIX` and `DATABASE_ACCOUNT_NAME` with the newly created RP resource group, and `DOMAIN_NAME` with the proper DNS name.

1. Update `kubernetes_resources/secrets.yaml` with your certificates
    1. Using the yaml provided, populate `admin-api-ca-bundle` and `arm-ca-bundle` fields with base64-encoded versions of these certificates, which were created during deploy, and can be found in `dev-config.yaml`.

1. In Azure Portal, move the database account created during `make deploy` into the AKS cluster resource group, named `*-aks1`.

1. In Azure Portal, move the DNS zones created during `make deploy` into the AKS cluster resource group, named `*-aks1`.

1. Grant the needed permissions to the AKS agentpool MSI in the `$USER-aro-$LOCATION-aks1` resource group (not to be confused with the AKS MSI, in the $USER-aro-$LOCATION resource group) To do this generally, navigate to `Portal > <name-of-resource> > Access Control (IAM) > Add role assignment > Owner`. The following permissions need to be granted to the AKS agentpool MSI:
    - `Owner` permissions on the `$USER-aro-$LOCATION` resource group
    - `Owner` permissions on the `$USER-gwy-$LOCATION` resource group
    - `Owner` permissions on the `$USER-aro-$LOCATION` database account

1. Connect to the VPN / vnet gateway created in an earlier step, and connect to the AKS cluster

    ```bash
    sudo openvpn secrets/vpn-$LOCATION.ovpn
    make aks.kubeconfig
    export KUBECONFIG=aks.kubeconfig
    kubectl get nodes
    ```
1. Apply Kubernetes resources

    ```bash
    kubectl apply -f kubernetes_resources/namespaces.yaml
    kubectl apply -f kubernetes_resources/secrets.yaml
    kubectl apply -f kubernetes_resources/configmaps.yaml
    kubectl apply -f kubernetes_resources/deployments.yaml
    kubectl apply -f kubernetes_resources/services.yaml
    ```
    
## Run requests against the RP pod

1. Using the pod IP of one of the RP replicas while connected to the VPN, OR using the service IP while in-cluster (debug pod or similar), you can make requests to the RP pod directly.

    ```bash
    export POD_IP="$(kubectl get pods -n aro-rp -o wide | awk '{ print $6 }' | tail -n +2 | head -1)"
    curl -X GET -k "https://$POD_IP:8443/admin/providers/microsoft.redhatopenshift/openshiftclusters"
    ```

## Create a cluster (WIP)

1. Register the subscription against the RP:

    ```bash
    curl -k -X PUT   -H 'Content-Type: application/json'   -d '{
        "state": "Registered",
        "properties": {
            "tenantId": "'"$AZURE_TENANT_ID"'",
            "registeredFeatures": [
                {
                    "name": "Microsoft.RedHatOpenShift/RedHatEngineering",
                    "state": "Registered"
                }
            ]
        }
    }' "https://$POD_IP:8443/subscriptions/$AZURE_SUBSCRIPTION_ID?api-version=2.0"
    ```

1. Create a service principal for use by the cluster: https://learn.microsoft.com/en-us/azure/openshift/howto-create-service-principal

    ```bash
    AZ_SUB_ID=$(az account show --query id -o tsv) 
    az ad sp create-for-rbac -n "test-aro-SP" --role contributor --scopes "/subscriptions/${RESOURCEGROUP}/resourceGroups/${RESOURCEGROUP}"
    ```

1. Set up cluster creation environment variables (this will overwrite the variables set previously, in env-int):

    ```bash
    LOCATION=eastus                 # the location of your cluster
    RESOURCEGROUP=aro-cluster       # the name of the resource group where you want to create your cluster
    CLUSTER=aro-cluster             # the name of your cluster
    POD_IP="$(kubectl get pods -n aro-rp -o wide | awk '{ print $6 }' | tail -n +2 | head -1)"
    CLIENT_ID=<UUID>                # the client id of the service principal created in the previous step
    CLIENT_SECRET=<UUID>            # the client secret of the service principal created in the previous step
    ```

1. Create resource group:

    ```bash
    az group create --name $RESOURCEGROUP --location $LOCATION
    ```

1. Create vnet and subnets:

    ```bash
    az network vnet create \
    --resource-group $RESOURCEGROUP \
    --name aro-vnet \
    --address-prefixes 10.0.0.0/22

    az network vnet subnet create \
    --resource-group $RESOURCEGROUP \
    --vnet-name aro-vnet \
    --name master-subnet \
    --address-prefixes 10.0.0.0/23

    az network vnet subnet create \
    --resource-group $RESOURCEGROUP \
    --vnet-name aro-vnet \
    --name worker-subnet \
    --address-prefixes 10.0.2.0/23
    ```

1. Create the cluster:

    ```bash
    curl -X PUT -k "https://$POD_IP:8443/subscriptions/$AZURE_SUBSCRIPTION_ID/resourceGroups/$RESOURCEGROUP/providers/Microsoft.RedHatOpenShift/openShiftClusters/$CLUSTER?api-version=2022-09-04" --header "Content-Type: application/json" -d '{"location": "'$LOCATION'", "properties": {"clusterProfile": {"pullSecret": "", "domain": "ncns1k70", "version": "", "resourceGroupId": "/subscriptions/'$AZURE_SUBSCRIPTION_ID'/resourceGroups/aro-ncns1k70", "fipsValidatedModules": "Disabled"}, "servicePrincipalProfile": {"clientId": "'$CLIENT_ID'", "clientSecret": "'$CLIENT_SECRET'"}, "networkProfile": {"podCidr": "10.128.0.0/14", "serviceCidr": "172.30.0.0/16"}, "masterProfile": {"vmSize": "Standard_D8s_v3", "subnetId": "/subscriptions/'$AZURE_SUBSCRIPTION_ID'/resourceGroups/'$RESOURCEGROUP'/providers/Microsoft.Network/virtualNetworks/aro-vnet/subnets/master-subnet", "encryptionAtHost": "Disabled"}, "workerProfiles": [{"name": "worker", "vmSize": "Standard_D4s_v3", "diskSizeGB": 128, "subnetId": "/subscriptions/'$AZURE_SUBSCRIPTION_ID'/resourceGroups/'$RESOURCEGROUP'/providers/Microsoft.Network/virtualNetworks/aro-vnet/subnets/worker-subnet", "count": 3, "encryptionAtHost": "Disabled"}], "apiserverProfile": {"visibility": "Public"}, "ingressProfiles": [{"name": "default", "visibility": "Public"}]}}'
    ```

## Recover VPN access

If you lose access to the vpn / vnet gateway that you provisioned in an earlier step (such as through a `make secrets`, or a change in VPN config), and you don't have a secrets/* backup, you can recover your access using the following steps. Please note that this action will _**OVER WRITE**_ the `secrets/vpn-$LOCATION.ovpn` on your local machine. **DO NOT** run `make secrets-update` after doing this, as you will overwrite existing config for all users.

1. Source all environment variables from earlier, and run the VPN configuration step again:

    ```bash
    . ./env
    . ./env-int

    source ./hack/devtools/deploy-shared-env.sh
    vpn_configuration
    ```

1. Create new VPN certificates locally:

    ```bash
    go run ./hack/genkey -ca vpn-ca
    mv vpn-ca.* secrets
    go run ./hack/genkey -client -keyFile secrets/vpn-ca.key -certFile secrets/vpn-ca.crt vpn-client
    mv vpn-client.* secrets
    ```

1. Update the VPN configuration locally:
    - Add the new cert and key created above (located in `secrets/vpn-client.pem`) to `secrets/vpn-eastus.ovpn`, replacing the existing configuration.

1. Add the newly created secrets to the `dev-vpn` vnet gateway in `$USER-aro-$LOCATION` resource group:
    - In portal, navigate to `dev-vpn`, Point-to-site configuration > Root certificates.
    - Add the new `secrets/vpn-ca.pem` data created above to this configuration.

1. Connect to the VPN:
    ```bash
    sudo openvpn secrets/vpn-$LOCATION.ovpn
    ```