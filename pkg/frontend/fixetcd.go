package frontend

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/go-autorest/autorest/to"
	operatorv1 "github.com/openshift/api/operator/v1"
	securityv1 "github.com/openshift/api/security/v1"
	operatorclient "github.com/openshift/client-go/operator/clientset/versioned"
	operatorv1client "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1"
	securityv1client "github.com/openshift/client-go/security/clientset/versioned/typed/security/v1"
	"github.com/sirupsen/logrus"
	"github.com/ugorji/go/codec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Azure/ARO-RP/pkg/api"
	"github.com/Azure/ARO-RP/pkg/env"
	"github.com/Azure/ARO-RP/pkg/frontend/adminactions"
	"github.com/Azure/ARO-RP/pkg/util/restconfig"
)

const (
	serviceAccountName                  = "etcd-recovery-privileged"
	kubeServiceAccount                  = "system:serviceaccount" + nameSpaceEtcds + ":" + serviceAccountName
	nameSpaceEtcds                      = "openshift-etcd"
	image                               = "ubi8/ubi-minimal"
	jobName                             = "etcd-recovery-"
	jobNameDataBackup                   = jobName + "data-backup"
	jobNameFixPeers                     = jobName + "fix-peers"
	patchOverides                       = "unsupportedConfigOverrides:"
	patchDisableOverrides               = `{"useUnsupportedUnsafeNonHANonProductionUnstableEtcd": true}`
	ctxKey                timeoutAdjust = "TESTING"
)

type timeoutAdjust string

type degradedEtcd struct {
	Node  string
	Pod   string
	NewIP string
	OldIP string
}

func (f *frontend) fixEtcd(ctx context.Context, log *logrus.Entry, env env.Interface, doc *api.OpenShiftClusterDocument, kubeActions adminactions.KubeActions, groupKind string) error {
	restConfig, err := restconfig.RestConfig(env, doc.OpenShiftCluster)
	if err != nil {
		return err
	}

	securitycli, err := securityv1client.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	operatorcli, err := operatorclient.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	rawPods, err := kubeActions.KubeList(ctx, "Pod", nameSpaceEtcds)
	if err != nil {
		return err
	}

	pods := &corev1.PodList{}
	err = codec.NewDecoderBytes(rawPods, &codec.JsonHandle{}).Decode(pods)
	if err != nil {
		return err
	}

	// Exit early if degradedEtcd is empty, an error is returned
	// no further actions should be taken
	de, err := comparePodEnvToIP(log, pods)
	if err != nil {
		return err
	}
	log.Infof("Found degraded endpoint: %v", de)

	err = backupEtcdData(ctx, log, doc.OpenShiftCluster.Name, de.Node, kubeActions)
	if err != nil {
		return err
	}

	err = fixPeers(ctx, log, de, pods, kubeActions, securitycli.SecurityContextConstraints(), doc.OpenShiftCluster.Name)
	if err != nil {
		return err
	}

	rawEtcd, err := kubeActions.KubeGet(ctx, groupKind, "", "cluster")
	if err != nil {
		return err
	}

	etcd := &operatorv1.Etcd{}
	err = codec.NewDecoderBytes(rawEtcd, &codec.JsonHandle{}).Decode(etcd)
	if err != nil {
		return err
	}

	etcd.Spec.UnsupportedConfigOverrides = kruntime.RawExtension{
		Raw: []byte(patchDisableOverrides),
	}
	err = patchEtcd(ctx, log, operatorcli.OperatorV1().Etcds(), etcd, patchDisableOverrides)
	if err != nil {
		return err
	}

	err = deleteSecrets(ctx, log, kubeActions, de)
	if err != nil {
		return err
	}

	etcd.Spec.ForceRedeploymentReason = fmt.Sprintf("single-master-recovery-%s", time.Now())
	err = patchEtcd(ctx, log, operatorcli.OperatorV1().Etcds(), etcd, etcd.Spec.ForceRedeploymentReason)
	if err != nil {
		return err
	}

	etcd.Spec.OperatorSpec.UnsupportedConfigOverrides.Reset()
	return patchEtcd(ctx, log, operatorcli.OperatorV1().Etcds(), etcd, patchOverides+string(etcd.Spec.OperatorSpec.UnsupportedConfigOverrides.Raw))
}

func patchEtcd(ctx context.Context, log *logrus.Entry, operatercli operatorv1client.EtcdInterface, e *operatorv1.Etcd, patch string) error {
	e.CreationTimestamp = metav1.Time{
		Time: time.Now(),
	}
	e.ResourceVersion = ""
	e.SelfLink = ""
	e.UID = ""

	buf := &bytes.Buffer{}
	err := codec.NewEncoder(buf, &codec.JsonHandle{}).Encode(e)
	if err != nil {
		return err
	}

	_, err = operatercli.Patch(ctx, e.Name, types.MergePatchType, buf.Bytes(), metav1.PatchOptions{})
	if err != nil {
		return err
	}
	log.Infof("Patched etcd %s with %s", e.Name, patch)

	return nil
}

func deleteSecrets(ctx context.Context, log *logrus.Entry, kubeActions adminactions.KubeActions, de *degradedEtcd) error {
	// TODO Replace all nameSpaceEtcds with parent function arguement namespace
	rawSecrets, err := kubeActions.KubeList(ctx, "Secret", nameSpaceEtcds)
	if err != nil {
		return err
	}

	secrets := corev1.SecretList{}
	err = codec.NewDecoderBytes(rawSecrets, &codec.JsonHandle{}).Decode(secrets)
	if err != nil {
		return err
	}

	for _, s := range secrets.Items {
		match, err := regexp.MatchString("."+de.Node+"$", s.Name)
		if err != nil {
			return nil
		}

		if match {
			log.Infof("Deleting secret %s", s.Name)
			err := kubeActions.KubeDelete(ctx, "Secret", nameSpaceEtcds, s.Name, false)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func getPeerPods(pods []corev1.Pod, de *degradedEtcd, cluster string) (string, error) {
	regNode, err := regexp.Compile(".master-[0-9]$")
	if err != nil {
		return "", err
	}
	regPod, err := regexp.Compile("etcd-" + cluster + "-[0-9A-Za-z]*-master-[0-9]$")
	if err != nil {
		return "", err
	}

	var peerPods string
	for _, p := range pods {
		if regNode.MatchString(p.Spec.NodeName) &&
			regPod.MatchString(p.Name) &&
			p.Name != de.Pod {
			peerPods += p.Name + " "
		}
	}
	return peerPods, nil
}

func fixPeers(ctx context.Context, log *logrus.Entry, de *degradedEtcd, pods *corev1.PodList, kubeActions adminactions.KubeActions, securitycli securityv1client.SecurityContextConstraintsInterface, cluster string) error {
	peerPods, err := getPeerPods(pods.Items, de, cluster)
	if err != nil {
		return err
	}

	scc, err := createPrivilegedServiceAccount(ctx, log, serviceAccountName, kubeServiceAccount, kubeActions, securitycli)
	if err != nil {
		return err
	}

	defer func() {
		log.Infof("Deleting service account %s now", serviceAccountName)
		err = kubeActions.KubeDelete(ctx, "ServiceAccount", nameSpaceEtcds, serviceAccountName, false)
	}()

	defer func() {
		log.Infof("Deleting %s now", scc.Name)
		err = securitycli.Delete(ctx, scc.Name, metav1.DeleteOptions{})
	}()

	defer func() {
		log.Infof("Deleting cluster role %s now", kubeServiceAccount)
		err = kubeActions.KubeDelete(ctx, "ClusterRole", nameSpaceEtcds, kubeServiceAccount, false)
	}()

	defer func() {
		log.Infof("Deleting cluster role binding %s now", serviceAccountName)
		err = kubeActions.KubeDelete(ctx, "ClusterRoleBinding", nameSpaceEtcds, serviceAccountName, false)
	}()

	log.Infof("Creating job %s", jobNameFixPeers)
	err = kubeActions.KubeCreateOrUpdate(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			jobNameFixPeers: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobNameFixPeers,
					Namespace: nameSpaceEtcds,
					Labels:    map[string]string{"app": jobNameDataBackup},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      jobNameFixPeers,
							Namespace: nameSpaceEtcds,
							Labels:    map[string]string{"app": jobNameDataBackup},
						},
						Spec: corev1.PodSpec{
							RestartPolicy:      corev1.RestartPolicyOnFailure,
							ServiceAccountName: serviceAccountName,
							Containers: []corev1.Container{
								{
									Name:  "backup",
									Image: image,
									Command: []string{
										"/bin/bash",
										"-cx",
										backupOrFixEtcd,
									},
									SecurityContext: &corev1.SecurityContext{
										Privileged: to.BoolPtr(true),
									},
									Env: []corev1.EnvVar{
										{
											Name:  "PEER_PODS",
											Value: peerPods,
										},
										{
											Name:  "DEGRADED_NODE",
											Value: de.Node,
										},
										{
											Name:  "FIX_PEERS",
											Value: "true",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "host",
											MountPath: "/host",
											ReadOnly:  false,
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "host",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{
											Path: "/",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return err
	}

	timeout := adjustTimeout(ctx, log)
	log.Infof("Waiting for %s", jobNameFixPeers)
	select {
	case <-time.After(timeout):
		log.Infof("Finished waiting for job %s", jobNameFixPeers)
		break
	case <-ctx.Done():
		log.Warnf("Request cancelled while waiting for %s", jobNameFixPeers)
	}

	log.Infof("Deleting %s now", jobNameFixPeers)
	//propPolicy := metav1.DeletePropagationBackground
	err = kubeActions.KubeDelete(ctx, "Job", nameSpaceEtcds, jobNameFixPeers, false)
	// TODO modify api to accept propagation policy
	//err = kubecli.BatchV1().Jobs(b.Namespace).Delete(ctx, jobNameFixPeers, metav1.DeleteOptions{
	//	PropagationPolicy: &propPolicy,
	//})
	if err != nil {
		return err
	}

	// return errors from deferred delete functions
	return err
}

func createPrivilegedServiceAccount(ctx context.Context, log *logrus.Entry, name, usersAccount string, kubeActions adminactions.KubeActions, securitycli securityv1client.SecurityContextConstraintsInterface) (*securityv1.SecurityContextConstraints, error) {
	err := kubeActions.KubeCreateOrUpdate(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			name: &corev1.ServiceAccount{
				TypeMeta: metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
				},
				AutomountServiceAccountToken: to.BoolPtr(true),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	err = kubeActions.KubeCreateOrUpdate(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			usersAccount: &rbacv1.ClusterRole{
				TypeMeta: metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{
					Name: usersAccount,
				},
				Rules: []rbacv1.PolicyRule{
					{
						// TODO restrict permissions to required
						Verbs:     []string{"*"},
						Resources: []string{"*"},
						APIGroups: []string{""},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	err = kubeActions.KubeCreateOrUpdate(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			serviceAccountName: &rbacv1.ClusterRoleBinding{
				TypeMeta: metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{
					Name: serviceAccountName,
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					Name:     kubeServiceAccount,
					APIGroup: "",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      serviceAccountName,
						Namespace: nameSpaceEtcds,
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	scc, err := securitycli.Get(ctx, "privileged", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	scc.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}
	scc.Groups = []string{}
	scc.Users = []string{usersAccount}
	scc.Namespace = nameSpaceEtcds

	log.Infof("Creating Security Context %s now", scc.Name)
	scc, err = securitycli.Create(ctx, scc, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	return scc, nil
}

func backupEtcdData(ctx context.Context, log *logrus.Entry, cluster, node string, kubeActions adminactions.KubeActions) error {
	jobDataBackup := &unstructured.Unstructured{
		Object: map[string]interface{}{
			jobNameDataBackup: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Job",
					APIVersion: "batch/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobNameDataBackup,
					Namespace: nameSpaceEtcds,
					Labels:    map[string]string{"app": jobNameDataBackup},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      jobNameDataBackup,
							Namespace: nameSpaceEtcds,
							Labels:    map[string]string{"app": jobNameDataBackup},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							NodeName:      node,
							Containers: []corev1.Container{
								{
									Name:  "backup",
									Image: image,
									Command: []string{
										"chroot",
										"/host",
										"/bin/bash",
										"-c",
										backupOrFixEtcd,
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "host",
											MountPath: "/host",
											ReadOnly:  false,
										},
									},
									SecurityContext: &corev1.SecurityContext{
										Capabilities: &corev1.Capabilities{
											Add: []corev1.Capability{"SYS_CHROOT"},
										},
										Privileged: to.BoolPtr(true),
									},
									Env: []corev1.EnvVar{
										{
											Name:  "BACKUP",
											Value: "true",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "host",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{
											Path: "/",
										},
									},
								},
							},
						},
					},
				},
			}},
	}

	//jobDataBackup.SetGroupVersionKind(
	//	kschema.GroupVersionKind{
	//		Group:   "batch",
	//		Kind:    "Job",
	//		Version: "v1",
	//	})
	jobDataBackup.SetKind("Job")
	jobDataBackup.SetAPIVersion("batch/v1")
	jobDataBackup.SetName(jobNameDataBackup)
	jobDataBackup.SetNamespace(nameSpaceEtcds)
	log.Infof("Setting job %s cluster name to: %s", jobNameDataBackup, cluster)
	jobDataBackup.SetClusterName(cluster)

	log.Infof("Creating job %s", jobNameDataBackup)
	err := kubeActions.KubeCreateOrUpdate(ctx, jobDataBackup)
	if err != nil {
		return err
	}
	log.Infof("Job %s has been created", jobNameDataBackup)

	timeout := adjustTimeout(ctx, log)
	log.Infof("Waiting for %s", jobNameDataBackup)
	select {
	case <-time.After(timeout):
		log.Infof("Finished waiting for job %s", jobNameDataBackup)
	case <-ctx.Done():
		log.Warnf("Request cancelled while waiting for %s", jobNameDataBackup)
	}

	log.Infof("Deleting job %s now", jobNameDataBackup)
	// TODO modify kube actions delete to allow delete options to be passed
	//propPolicy := metav1.DeletePropagationBackground
	//return batch.Jobs(nameSpaceEtcds).Delete(ctx, jobNameDataBackup, metav1.DeleteOptions{
	//	PropagationPolicy: &propPolicy,
	//})
	return kubeActions.KubeDelete(ctx, "Job", nameSpaceEtcds, jobNameDataBackup, false)
}

func adjustTimeout(ctx context.Context, log *logrus.Entry) time.Duration {
	if ctx.Value(ctxKey) == "TRUE" {
		log.Info("Timeout adjusted for testing environment")
		return time.Microsecond
	}

	return time.Minute
}

func comparePodEnvToIP(log *logrus.Entry, pods *corev1.PodList) (*degradedEtcd, error) {
	for _, p := range pods.Items {
		etcdIP := ipFromEnv(p.Spec.Containers, p.Name)
		for _, ip := range p.Status.PodIPs {
			if ip.IP != etcdIP && etcdIP != "" {
				log.Infof("Found conflicting IPs for etcd Pod %s: %s!=%s", p.Name, ip.IP, etcdIP)
				return &degradedEtcd{
					Node:  strings.ReplaceAll(p.Name, "etcd-", ""),
					Pod:   p.Name,
					NewIP: ip.IP,
					OldIP: etcdIP,
				}, nil
			}
		}
	}
	return &degradedEtcd{}, errors.New("degradedEtcd is empty, unable to remediate etcd deployment")
}

func ipFromEnv(containers []corev1.Container, podName string) string {
	for _, c := range containers {
		if c.Name == "etcd" {
			for _, e := range c.Env {
				envName := strings.ReplaceAll(strings.ReplaceAll(podName, "-", "_"), "etcd_", "NODE_")
				if e.Name == fmt.Sprintf("%s_IP", envName) {
					return e.Value
				}
			}
		}
	}
	return ""
}
