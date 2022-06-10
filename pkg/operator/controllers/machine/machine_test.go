package machine

import (
	"context"
	"testing"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/Azure/ARO-RP/pkg/api"
	"github.com/Azure/ARO-RP/pkg/api/validate"
	_ "github.com/Azure/ARO-RP/pkg/util/scheme"
)

var machine = &machinev1beta1.Machine{
	ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: machineSetsNamespace,
		Labels:    labels,
	},
	Spec: machinev1beta1.MachineSpec{
		ProviderSpec: machinev1beta1.ProviderSpec{
			Value: &kruntime.RawExtension{
				Raw: []byte(fmt.Sprintf(`{
"apiVersion": "machine.openshift.io/v1beta1",
"kind": "AzureMachineProviderSpec",
"osDisk": {
"diskSizeGB": %v
},
"image": {
"publisher": "%v",
"offer": "%v"
},
"vmSize": "%v"
}`, diskSize, imagePublisher, offer, vmSize))},
		},
	},
}

func TestWorkerReplicas(t *testing.T) {
	baseCluster := arov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status:     arov1alpha1.ClusterStatus{Conditions: []operatorv1.OperatorCondition{}},
		Spec: arov1alpha1.ClusterSpec{
			OperatorFlags: arov1alpha1.OperatorFlags{
				controllerEnabled: "true",
			},
		},
	}

	tests := []struct {
	}{
		{
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Reconcile(context.Background(), tt.request)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}