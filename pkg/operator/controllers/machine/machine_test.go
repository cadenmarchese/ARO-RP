package machine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	arov1alpha1 "github.com/Azure/ARO-RP/pkg/operator/apis/aro.openshift.io/v1alpha1"
	arofake "github.com/Azure/ARO-RP/pkg/operator/clientset/versioned/fake"
	"github.com/Azure/go-autorest/autorest/to"
	machinefake "github.com/openshift/client-go/machine/clientset/versioned/fake"
)

var fakeMachineSet = func(replicas int32) *machinefake.Clientset {
	machineset := &machinev1beta1.MachineSet{
		Spec: machinev1beta1.MachineSetSpec{
			Replicas: to.Int32Ptr(replicas),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-machine-api",
		},
	}
	return machinefake.NewSimpleClientset(machineset)
}

var fakeMachine0 = func(providerSpec *runtime.RawExtension) *machinev1beta1.Machine {
	return &machinev1beta1.Machine{
		Spec: machinev1beta1.MachineSpec{
			ProviderSpec: machinev1beta1.ProviderSpec{providerSpec},
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sillymachine",
			Namespace: "openshift-machine-api",
		},
	}
}

var fakeMachine1 = func(label, role string) *machinev1beta1.Machine {
	return &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "othermachine",
			Labels: map[string]string{label: role},
		},
	}
}

func TestWorkerReplicas(t *testing.T) {
	tests := []struct {
		name         string
		maocli       *machinefake.Clientset
		wantReplicas int32
		wantErr      error
	}{
		{
			name:         "worker replicas is 3",
			maocli:       fakeMachineSet(3),
			wantReplicas: 3,
			wantErr:      nil,
		},
		{
			name:         "worker replicas is 1",
			maocli:       fakeMachineSet(1),
			wantReplicas: 1,
			wantErr:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				maocli:                 tt.maocli,
				log:                    logrus.NewEntry(logrus.StandardLogger()),
				arocli:                 arofake.NewSimpleClientset(&arov1alpha1.Cluster{}),
				isLocalDevelopmentMode: false,
				role:                   "worker",
			}

			actualReplicas, err := r.workerReplicas(context.Background())
			if err != nil || actualReplicas != int(tt.wantReplicas) {
				t.Fatalf("err %v: wanted %v, got %v", err, tt.wantReplicas, actualReplicas)
			}
		})
	}
}

func TestMachineValid(t *testing.T) {
	tests := []struct {
		name     string
		maocli   *machinefake.Clientset
		machine  machinev1beta1.Machine
		isMaster bool
		wantErr  []error
	}{
		{
			name:    "provider spec missing",
			machine: *fakeMachine0(nil),
			wantErr: []error{fmt.Errorf("machine sillymachine: provider spec missing")},
		},
		{
			name: "provider spec present",
			machine: *fakeMachine0(&runtime.RawExtension{
				Object: &machinev1beta1.AzureMachineProviderSpec{
					TypeMeta: metav1.TypeMeta{
						Kind: "some kind",
					},
				},
			}),
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := "worker"
			if tt.isMaster {
				role = "master"
			}

			r := &Reconciler{
				maocli:                 tt.maocli,
				log:                    logrus.NewEntry(logrus.StandardLogger()),
				arocli:                 arofake.NewSimpleClientset(&arov1alpha1.Cluster{}),
				isLocalDevelopmentMode: false,
				role:                   role,
			}

			errs := r.machineValid(context.Background(), &tt.machine, tt.isMaster)
			if !reflect.DeepEqual(tt.wantErr, errs) {
				t.Fatalf("wanted %v, got %v", tt.wantErr, errs)
			}
		})
	}
}

func TestIsMasterRole(t *testing.T) {
	tests := []struct {
		name       string
		machine    machinev1beta1.Machine
		wantResult bool
		wantErr    error
	}{
		{
			name:       "machine is a master",
			machine:    *fakeMachine1("machine.openshift.io/cluster-api-machine-role", "master"),
			wantResult: true,
			wantErr:    nil,
		},
		{
			name:       "machine is not a master",
			machine:    *fakeMachine1("machine.openshift.io/cluster-api-machine-role", "worker"),
			wantResult: false,
			wantErr:    nil,
		},
		{
			name:       "label not found",
			machine:    *fakeMachine1("", ""),
			wantResult: false,
			wantErr:    errors.New("machine othermachine: cluster-api-machine-role label not found"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := isMasterRole(&tt.machine)
			if result != tt.wantResult {
				t.Fatalf("wanted %v, got %v", tt.wantResult, result)
			}

			if !reflect.DeepEqual(err, tt.wantErr) {
				t.Fatalf("wanted %v, got %v", tt.wantErr, err)
			}
		})
	}
}
