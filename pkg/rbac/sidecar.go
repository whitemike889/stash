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

package rbac

import (
	"context"
	"strings"

	"stash.appscode.dev/apimachinery/apis"
	api "stash.appscode.dev/apimachinery/apis/stash/v1alpha1"
	api_v1beta1 "stash.appscode.dev/apimachinery/apis/stash/v1beta1"
	"stash.appscode.dev/stash/pkg/util"

	"github.com/appscode/go/log"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/apis/batch"
	core_util "kmodules.xyz/client-go/core/v1"
	meta_util "kmodules.xyz/client-go/meta"
	rbac_util "kmodules.xyz/client-go/rbac/v1"
	wapi "kmodules.xyz/webhook-runtime/apis/workload/v1"
)

func getSidecarRoleBindingName(name string, kind string) string {
	return meta_util.ValidNameWithPrefixNSuffix(apis.StashSidecarClusterRole, strings.ToLower(kind), name)
}

func EnsureSidecarClusterRole(kc kubernetes.Interface) error {
	meta := metav1.ObjectMeta{Name: apis.StashSidecarClusterRole}
	_, _, err := rbac_util.CreateOrPatchClusterRole(context.TODO(), kc, meta, func(in *rbac.ClusterRole) *rbac.ClusterRole {
		if in.Labels == nil {
			in.Labels = map[string]string{}
		}
		in.Labels[apis.LabelApp] = apis.AppLabelStash

		in.Rules = []rbac.PolicyRule{
			{
				APIGroups: []string{api.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{api_v1beta1.SchemeGroupVersion.Group},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "patch"},
			},
			{
				APIGroups: []string{apps.GroupName},
				Resources: []string{"daemonsets", "replicasets"},
				Verbs:     []string{"get", "list", "patch"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"replicationcontrollers"},
				Verbs:     []string{"get", "list", "patch"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"secrets", "pods"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"get", "create"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"configmaps"},
				Verbs:     []string{"create", "update", "get"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"events"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{batch.GroupName},
				Resources: []string{"jobs"},
				Verbs:     []string{"create", "get"},
			},
			{
				APIGroups: []string{rbac.GroupName},
				Resources: []string{"clusterroles", "roles", "rolebindings"},
				Verbs:     []string{"get", "create"},
			},
			{
				APIGroups: []string{core.GroupName},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"get", "create"},
			},
		}
		return in
	}, metav1.PatchOptions{})
	return err
}

func EnsureSidecarRoleBinding(kc kubernetes.Interface, owner *metav1.OwnerReference, namespace, sa string, labels map[string]string) error {
	meta := metav1.ObjectMeta{
		Namespace: namespace,
		Name:      getSidecarRoleBindingName(owner.Name, owner.Kind),
		Labels:    labels,
	}
	_, _, err := rbac_util.CreateOrPatchRoleBinding(context.TODO(), kc, meta, func(in *rbac.RoleBinding) *rbac.RoleBinding {
		core_util.EnsureOwnerReference(&in.ObjectMeta, owner)

		if in.Annotations == nil {
			in.Annotations = map[string]string{}
		}

		in.RoleRef = rbac.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     apis.KindClusterRole,
			Name:     apis.StashSidecarClusterRole,
		}
		in.Subjects = []rbac.Subject{
			{
				Kind:      rbac.ServiceAccountKind,
				Name:      sa,
				Namespace: namespace,
			},
		}
		return in
	}, metav1.PatchOptions{})
	return err
}

func ensureSidecarRoleBindingDeleted(kubeClient kubernetes.Interface, w *wapi.Workload) error {
	err := kubeClient.RbacV1().RoleBindings(w.Namespace).Delete(
		context.TODO(),
		getSidecarRoleBindingName(w.Name, w.Kind),
		metav1.DeleteOptions{})
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}
	if err == nil {
		log.Infof("RoleBinding %s/%s has been deleted", w.Namespace, getSidecarRoleBindingName(w.Name, w.Kind))
	}
	return nil
}

func EnsureUnnecessaryWorkloadRBACDeleted(kubeClient kubernetes.Interface, w *wapi.Workload) error {
	// delete backup sidecar RoleBinding if workload does not have stash sidecar
	if !util.HasStashSidecar(w.Spec.Template.Spec.Containers) {
		err := ensureSidecarRoleBindingDeleted(kubeClient, w)
		if err != nil && !kerr.IsNotFound(err) {
			return err
		}
	}

	// delete restore init-container RoleBinding if workload does not have sash init-container
	if !util.HasStashInitContainer(w.Spec.Template.Spec.InitContainers) {
		err := ensureRestoreInitContainerRoleBindingDeleted(kubeClient, w)
		if err != nil && !kerr.IsNotFound(err) {
			return err
		}
	}

	return nil
}
