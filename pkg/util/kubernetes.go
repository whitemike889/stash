/*
Copyright AppsCode Inc. and Contributors

Licensed under the AppsCode Community License 1.0.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://github.com/appscode/licenses/raw/1.0.0/AppsCode-Community-1.0.0.md

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"stash.appscode.dev/apimachinery/apis"
	api "stash.appscode.dev/apimachinery/apis/stash/v1alpha1"
	v1beta1_api "stash.appscode.dev/apimachinery/apis/stash/v1beta1"

	"github.com/appscode/go/log"
	"github.com/appscode/go/types"
	snapshot_cs "github.com/kubernetes-csi/external-snapshotter/v2/pkg/client/clientset/versioned"
	core "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/meta"
	store "kmodules.xyz/objectstore-api/api/v1"
	oc_cs "kmodules.xyz/openshift/client/clientset/versioned"
	wapi "kmodules.xyz/webhook-runtime/apis/workload/v1"
)

func IsBackupTarget(target *v1beta1_api.BackupTarget, w *wapi.Workload) bool {
	if target != nil &&
		target.Ref.APIVersion == w.APIVersion &&
		target.Ref.Kind == w.Kind &&
		target.Ref.Name == w.Name {
		return true
	}
	return false
}

func IsRestoreTarget(target *v1beta1_api.RestoreTarget, w *wapi.Workload) bool {
	if target != nil &&
		target.Ref.APIVersion == w.APIVersion &&
		target.Ref.Kind == w.Kind &&
		target.Ref.Name == w.Name {
		return true
	}
	return false
}

func GetString(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

func UpsertScratchVolume(volumes []core.Volume) []core.Volume {
	return core_util.UpsertVolume(volumes, core.Volume{
		Name: apis.ScratchDirVolumeName,
		VolumeSource: core.VolumeSource{
			EmptyDir: &core.EmptyDirVolumeSource{},
		},
	})
}

func UpsertTmpVolume(volumes []core.Volume, settings v1beta1_api.EmptyDirSettings) []core.Volume {
	return core_util.UpsertVolume(volumes, core.Volume{
		Name: apis.TmpDirVolumeName,
		VolumeSource: core.VolumeSource{
			EmptyDir: &core.EmptyDirVolumeSource{
				Medium:    settings.Medium,
				SizeLimit: settings.SizeLimit,
			},
		},
	})
}

func UpsertTmpVolumeMount(volumeMounts []core.VolumeMount) []core.VolumeMount {
	return core_util.UpsertVolumeMountByPath(volumeMounts, core.VolumeMount{
		Name:      apis.TmpDirVolumeName,
		MountPath: apis.TmpDirMountPath,
	})
}

// https://kubernetes.io/docs/tasks/inject-data-application/downward-api-volume-expose-pod-information/#store-pod-fields
func UpsertDownwardVolume(volumes []core.Volume) []core.Volume {
	return core_util.UpsertVolume(volumes, core.Volume{
		Name: apis.PodinfoVolumeName,
		VolumeSource: core.VolumeSource{
			DownwardAPI: &core.DownwardAPIVolumeSource{
				Items: []core.DownwardAPIVolumeFile{
					{
						Path: "labels",
						FieldRef: &core.ObjectFieldSelector{
							FieldPath: "metadata.labels",
						},
					},
				},
			},
		},
	})
}

func UpsertSecretVolume(volumes []core.Volume, secretName string) []core.Volume {
	return core_util.UpsertVolume(volumes, core.Volume{
		Name: apis.StashSecretVolume,
		VolumeSource: core.VolumeSource{
			Secret: &core.SecretVolumeSource{
				SecretName: secretName,
			},
		},
	})
}

// UpsertSecurityContext update current SecurityContext with new SecurityContext.
// If a field is not present in the new SecurityContext, value of the current SecurityContext for this field will be used.
func UpsertSecurityContext(currentSC, newSC *core.SecurityContext) *core.SecurityContext {
	if newSC == nil {
		return currentSC
	}

	var finalSC *core.SecurityContext
	if currentSC == nil {
		finalSC = &core.SecurityContext{}
	} else {
		finalSC = currentSC.DeepCopy()
	}

	if newSC.Capabilities != nil {
		finalSC.Capabilities = newSC.Capabilities
	}
	if newSC.Privileged != nil {
		finalSC.Privileged = newSC.Privileged
	}
	if newSC.SELinuxOptions != nil {
		finalSC.SELinuxOptions = newSC.SELinuxOptions
	}
	if newSC.RunAsUser != nil {
		finalSC.RunAsUser = newSC.RunAsUser
	}
	if newSC.RunAsGroup != nil {
		finalSC.RunAsGroup = newSC.RunAsGroup
	}
	if newSC.RunAsNonRoot != nil {
		finalSC.RunAsNonRoot = newSC.RunAsNonRoot
	}
	if newSC.ReadOnlyRootFilesystem != nil {
		finalSC.ReadOnlyRootFilesystem = newSC.ReadOnlyRootFilesystem
	}
	if newSC.AllowPrivilegeEscalation != nil {
		finalSC.AllowPrivilegeEscalation = newSC.AllowPrivilegeEscalation
	}
	if newSC.ProcMount != nil {
		finalSC.ProcMount = newSC.ProcMount
	}

	return finalSC
}

// UpsertPodSecurityContext update current SecurityContext with new SecurityContext.
// If a field is not present in the new SecurityContext, value of the current SecurityContext for this field will be used.
func UpsertPodSecurityContext(currentSC, newSC *core.PodSecurityContext) *core.PodSecurityContext {
	if newSC == nil {
		return currentSC
	}

	var finalSC *core.PodSecurityContext
	if currentSC == nil {
		finalSC = &core.PodSecurityContext{}
	} else {
		finalSC = currentSC.DeepCopy()
	}

	if newSC.SELinuxOptions != nil {
		finalSC.SELinuxOptions = newSC.SELinuxOptions
	}
	if newSC.RunAsUser != nil {
		finalSC.RunAsUser = newSC.RunAsUser
	}
	if newSC.RunAsGroup != nil {
		finalSC.RunAsGroup = newSC.RunAsGroup
	}
	if newSC.RunAsNonRoot != nil {
		finalSC.RunAsNonRoot = newSC.RunAsNonRoot
	}
	if newSC.SupplementalGroups != nil {
		finalSC.SupplementalGroups = newSC.SupplementalGroups
	}
	if newSC.FSGroup != nil {
		finalSC.FSGroup = newSC.FSGroup
	}
	if newSC.Sysctls != nil {
		finalSC.Sysctls = newSC.Sysctls
	}

	return finalSC
}

func MergeLocalVolume(volumes []core.Volume, backend *store.Backend, volName string) []core.Volume {
	// check if the local volume already exist
	oldPos := -1
	for i, vol := range volumes {
		if vol.Name == volName {
			oldPos = i
			break
		}
	}

	if backend != nil && backend.Local != nil {
		// backend is local backend. we have to mount the local volume inside sidecar
		vol, _ := backend.Local.ToVolumeAndMount(volName)
		if oldPos != -1 {
			volumes[oldPos] = vol
		} else {
			volumes = core_util.UpsertVolume(volumes, vol)
		}
	} else {
		// backend is not local backend. we have to remove stash-local volume if we had mounted before
		if oldPos != -1 {
			volumes = append(volumes[:oldPos], volumes[oldPos+1:]...)
		}
	}
	return volumes
}

func EnsureVolumeDeleted(volumes []core.Volume, name string) []core.Volume {
	for i, v := range volumes {
		if v.Name == name {
			return append(volumes[:i], volumes[i+1:]...)
		}
	}
	return volumes
}

func RecoveryEqual(old, new *api.Recovery) bool {
	var oldSpec, newSpec *api.RecoverySpec
	if old != nil {
		oldSpec = &old.Spec
	}
	if new != nil {
		newSpec = &new.Spec
	}
	return reflect.DeepEqual(oldSpec, newSpec)
}

func WorkloadExists(k8sClient kubernetes.Interface, namespace string, workload api.LocalTypedReference) error {
	if err := workload.Canonicalize(); err != nil {
		return err
	}

	switch workload.Kind {
	case apis.KindDeployment:
		_, err := k8sClient.AppsV1().Deployments(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		return err
	case apis.KindReplicaSet:
		_, err := k8sClient.AppsV1().ReplicaSets(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		return err
	case apis.KindReplicationController:
		_, err := k8sClient.CoreV1().ReplicationControllers(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		return err
	case apis.KindStatefulSet:
		_, err := k8sClient.AppsV1().StatefulSets(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		return err
	case apis.KindDaemonSet:
		_, err := k8sClient.AppsV1().DaemonSets(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		return err
	default:
		return fmt.Errorf(`unrecognized workload "Kind" %v`, workload.Kind)
	}
}

func GetConfigmapLockName(workload api.LocalTypedReference) string {
	return strings.ToLower(fmt.Sprintf("lock-%s-%s", workload.Kind, workload.Name))
}

func GetBackupConfigmapLockName(r v1beta1_api.TargetRef) string {
	return strings.ToLower(fmt.Sprintf("lock-%s-%s-backup", r.Kind, r.Name))
}

func GetRestoreConfigmapLockName(r v1beta1_api.TargetRef) string {
	return strings.ToLower(fmt.Sprintf("lock-%s-%s-restore", r.Kind, r.Name))
}

func DeleteConfigmapLock(k8sClient kubernetes.Interface, namespace string, workload api.LocalTypedReference) error {
	return k8sClient.CoreV1().ConfigMaps(namespace).Delete(context.TODO(), GetConfigmapLockName(workload), metav1.DeleteOptions{})
}

func DeleteBackupConfigMapLock(k8sClient kubernetes.Interface, namespace string, r v1beta1_api.TargetRef) error {
	return k8sClient.CoreV1().ConfigMaps(namespace).Delete(context.TODO(), GetBackupConfigmapLockName(r), metav1.DeleteOptions{})
}

func DeleteRestoreConfigMapLock(k8sClient kubernetes.Interface, namespace string, r v1beta1_api.TargetRef) error {
	return k8sClient.CoreV1().ConfigMaps(namespace).Delete(context.TODO(), GetRestoreConfigmapLockName(r), metav1.DeleteOptions{})
}

func DeleteAllConfigMapLocks(k8sClient kubernetes.Interface, namespace, name, kind string) error {
	// delete backup configMap lock if exist
	err := DeleteBackupConfigMapLock(k8sClient, namespace, v1beta1_api.TargetRef{Name: name, Kind: kind})
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}
	// delete restore configMap lock if exist
	err = DeleteRestoreConfigMapLock(k8sClient, namespace, v1beta1_api.TargetRef{Name: name, Kind: kind})
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}
	// backward compatibility
	err = DeleteConfigmapLock(k8sClient, namespace, api.LocalTypedReference{Kind: kind, Name: name})
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}
	return nil
}

func WorkloadReplicas(kubeClient *kubernetes.Clientset, namespace string, workloadKind string, workloadName string) (int32, error) {
	switch workloadKind {
	case apis.KindDeployment:
		obj, err := kubeClient.AppsV1().Deployments(namespace).Get(context.TODO(), workloadName, metav1.GetOptions{})
		if err != nil {
			return 0, err
		} else {
			return *obj.Spec.Replicas, nil
		}
	case apis.KindReplicationController:
		obj, err := kubeClient.CoreV1().ReplicationControllers(namespace).Get(context.TODO(), workloadName, metav1.GetOptions{})
		if err != nil {
			return 0, err
		} else {
			return *obj.Spec.Replicas, nil
		}
	case apis.KindReplicaSet:
		obj, err := kubeClient.AppsV1().ReplicaSets(namespace).Get(context.TODO(), workloadName, metav1.GetOptions{})
		if err != nil {
			return 0, err
		} else {
			return *obj.Spec.Replicas, nil
		}

	default:
		return 0, fmt.Errorf("unknown workload type")
	}
}

func HasOldReplicaAnnotation(k8sClient *kubernetes.Clientset, namespace string, workload api.LocalTypedReference) bool {
	var workloadAnnotation map[string]string

	switch workload.Kind {
	case apis.KindDeployment:
		obj, err := k8sClient.AppsV1().Deployments(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		if err != nil {
			log.Fatalln(err)
		}
		workloadAnnotation = obj.Annotations
	case apis.KindReplicationController:
		obj, err := k8sClient.CoreV1().ReplicationControllers(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		if err != nil {
			log.Fatalln(err)
		}
		workloadAnnotation = obj.Annotations
	case apis.KindReplicaSet:
		obj, err := k8sClient.AppsV1().ReplicaSets(namespace).Get(context.TODO(), workload.Name, metav1.GetOptions{})
		if err != nil {
			log.Fatalln(err)
		}
		workloadAnnotation = obj.Annotations
	case apis.KindStatefulSet:
		// do nothing. we didn't scale down.
	case apis.KindDaemonSet:
		// do nothing.
	default:
		return false

	}

	return meta.HasKey(workloadAnnotation, apis.AnnotationOldReplica)
}

func WaitUntilDeploymentReady(c kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := c.AppsV1().Deployments(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return types.Int32(obj.Spec.Replicas) == obj.Status.ReadyReplicas, nil
		}
		return false, nil
	})
}

func WaitUntilDaemonSetReady(kubeClient kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := kubeClient.AppsV1().DaemonSets(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return obj.Status.DesiredNumberScheduled == obj.Status.NumberReady, nil
		}
		return false, nil
	})
}

func WaitUntilReplicaSetReady(c kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := c.AppsV1().ReplicaSets(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return types.Int32(obj.Spec.Replicas) == obj.Status.ReadyReplicas, nil
		}
		return false, nil
	})
}

func WaitUntilRCReady(c kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := c.CoreV1().ReplicationControllers(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return types.Int32(obj.Spec.Replicas) == obj.Status.ReadyReplicas, nil
		}

		return false, nil
	})
}

func WaitUntilStatefulSetReady(kubeClient kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := kubeClient.AppsV1().StatefulSets(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return types.Int32(obj.Spec.Replicas) == obj.Status.ReadyReplicas, nil
		}
		return false, nil
	})
}

func WaitUntilDeploymentConfigReady(c oc_cs.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, apis.ReadinessTimeout, func() (bool, error) {
		if obj, err := c.AppsV1().DeploymentConfigs(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return obj.Spec.Replicas == obj.Status.ReadyReplicas, nil
		}
		return false, nil
	})
}

func WaitUntilVolumeSnapshotReady(c snapshot_cs.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, 2*time.Hour, func() (bool, error) {
		if obj, err := c.SnapshotV1beta1().VolumeSnapshots(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return obj.Status != nil && obj.Status.ReadyToUse != nil && *obj.Status.ReadyToUse, nil
		}
		return false, nil
	})
}

func WaitUntilPVCReady(c kubernetes.Interface, meta metav1.ObjectMeta) error {
	return wait.PollImmediate(apis.RetryInterval, 2*time.Hour, func() (bool, error) {
		if obj, err := c.CoreV1().PersistentVolumeClaims(meta.Namespace).Get(context.TODO(), meta.Name, metav1.GetOptions{}); err == nil {
			return obj.Status.Phase == core.ClaimBound, nil
		}
		return false, nil
	})
}
