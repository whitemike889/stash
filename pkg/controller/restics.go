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

package controller

import (
	"context"
	"fmt"

	"stash.appscode.dev/apimachinery/apis"
	"stash.appscode.dev/apimachinery/apis/stash"
	api "stash.appscode.dev/apimachinery/apis/stash/v1alpha1"
	"stash.appscode.dev/apimachinery/pkg/docker"
	"stash.appscode.dev/stash/pkg/eventer"
	stash_rbac "stash.appscode.dev/stash/pkg/rbac"
	"stash.appscode.dev/stash/pkg/util"

	"github.com/appscode/go/log"
	"github.com/golang/glog"
	batch "k8s.io/api/batch/v1beta1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/reference"
	apps_util "kmodules.xyz/client-go/apps/v1"
	batch_util "kmodules.xyz/client-go/batch/v1beta1"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/tools/queue"
	"kmodules.xyz/webhook-runtime/admission"
	hooks "kmodules.xyz/webhook-runtime/admission/v1beta1"
	webhook "kmodules.xyz/webhook-runtime/admission/v1beta1/generic"
)

// TODO: Add validator that will reject to create Restic if any BackupConfiguration exist for target workload

func (c *StashController) NewResticWebhook() hooks.AdmissionHook {
	return webhook.NewGenericWebhook(
		schema.GroupVersionResource{
			Group:    "admission.stash.appscode.com",
			Version:  "v1alpha1",
			Resource: "resticvalidators",
		},
		"resticvalidator",
		[]string{stash.GroupName},
		api.SchemeGroupVersion.WithKind("Restic"),
		nil,
		&admission.ResourceHandlerFuncs{
			CreateFunc: func(obj runtime.Object) (runtime.Object, error) {
				return nil, obj.(*api.Restic).IsValid()
			},
			UpdateFunc: func(oldObj, newObj runtime.Object) (runtime.Object, error) {
				return nil, newObj.(*api.Restic).IsValid()
			},
		},
	)
}

func (c *StashController) initResticWatcher() {
	c.rstInformer = c.stashInformerFactory.Stash().V1alpha1().Restics().Informer()
	c.rstQueue = queue.New("Restic", c.MaxNumRequeues, c.NumThreads, c.runResticInjector)
	c.rstInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if r, ok := obj.(*api.Restic); ok {
				if err := r.IsValid(); err != nil {
					ref, rerr := reference.GetReference(scheme.Scheme, r)
					if rerr == nil {
						c.recorder.Eventf(
							ref,
							core.EventTypeWarning,
							eventer.EventReasonInvalidRestic,
							"Reason %v",
							err,
						)
					}
					return
				}
				queue.Enqueue(c.rstQueue.GetQueue(), obj)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldRes, ok := oldObj.(*api.Restic)
			if !ok {
				log.Errorln("Invalid Restic object")
				return
			}
			newRes, ok := newObj.(*api.Restic)
			if !ok {
				log.Errorln("Invalid Restic object")
				return
			}
			if err := newRes.IsValid(); err != nil {
				ref, rerr := reference.GetReference(scheme.Scheme, newRes)
				if rerr == nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason %v",
						err,
					)
				}
				return
			} else if !util.ResticEqual(oldRes, newRes) {
				queue.Enqueue(c.rstQueue.GetQueue(), newObj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			queue.Enqueue(c.rstQueue.GetQueue(), obj)
		},
	})
	c.rstLister = c.stashInformerFactory.Stash().V1alpha1().Restics().Lister()
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the deployment to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *StashController) runResticInjector(key string) error {
	obj, exists, err := c.rstInformer.GetIndexer().GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with a Restic, so that we will see a delete for one d
		glog.Warningf("Restic %s does not exist anymore\n", key)

		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			return err
		}
		c.EnsureSidecarDeleted(namespace, name)
	} else {
		restic := obj.(*api.Restic)
		glog.Infof("Sync/Add/Update for Restic %s", restic.GetName())

		if restic.Spec.Type == api.BackupOffline {
			err = c.EnsureScaledownCronJob(restic)
			if err != nil {
				return err
			}
		}
		c.EnsureSidecar(restic)
		c.EnsureSidecarDeleted(restic.Namespace, restic.Name)
	}
	return nil
}

func (c *StashController) EnsureScaledownCronJob(restic *api.Restic) error {
	image := docker.Docker{
		Registry: c.DockerRegistry,
		Image:    c.StashImage,
		Tag:      c.StashImageTag,
	}

	meta := metav1.ObjectMeta{
		Name:      apis.ScaledownCronPrefix + restic.Name,
		Namespace: restic.Namespace,
	}

	selector, err := metav1.LabelSelectorAsSelector(&restic.Spec.Selector)
	if err != nil {
		return err
	}

	cronJob, _, err := batch_util.CreateOrPatchCronJob(
		context.TODO(),
		c.kubeClient,
		meta,
		func(in *batch.CronJob) *batch.CronJob {
			// set restic as cron-job owner
			in.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: api.SchemeGroupVersion.String(),
					Kind:       api.ResourceKindRestic,
					Name:       restic.Name,
					UID:        restic.UID,
				},
			}

			if in.Labels == nil {
				in.Labels = map[string]string{}
			}
			in.Labels[apis.AnnotationRestic] = restic.Name
			in.Labels[apis.AnnotationOperation] = apis.OperationScaleDown
			// ensure job gets deleted on completion
			in.Labels[apis.KeyDeleteJobOnCompletion] = apis.AllowDeletingJobOnCompletion

			// spec
			in.Spec.Schedule = restic.Spec.Schedule
			if in.Spec.JobTemplate.Labels == nil {
				in.Spec.JobTemplate.Labels = map[string]string{}
			}
			in.Spec.JobTemplate.Labels[apis.LabelApp] = apis.AppLabelStash
			in.Spec.JobTemplate.Labels[apis.AnnotationRestic] = restic.Name
			in.Spec.JobTemplate.Labels[apis.AnnotationOperation] = apis.OperationScaleDown

			in.Spec.JobTemplate.Spec.Template.Spec.Containers = core_util.UpsertContainer(
				in.Spec.JobTemplate.Spec.Template.Spec.Containers,
				core.Container{
					Name:  apis.StashContainer,
					Image: image.ToContainerImage(),
					Args: []string{
						"scaledown",
						"--selector=" + selector.String(),
					},
				})
			in.Spec.JobTemplate.Spec.Template.Spec.ImagePullSecrets = restic.Spec.ImagePullSecrets

			in.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = core.RestartPolicyNever
			in.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName = in.Name
			return in
		},
		metav1.PatchOptions{},
	)
	if err != nil {
		return err
	}

	owner := metav1.NewControllerRef(cronJob, batch.SchemeGroupVersion.WithKind(apis.KindCronJob))
	if err = stash_rbac.EnsureScaledownJobRBAC(c.kubeClient, owner, cronJob.Namespace); err != nil {
		return fmt.Errorf("error ensuring rbac for kubectl cron job %s, reason: %s", meta.Name, err)
	}

	return nil
}

func (c *StashController) EnsureSidecar(restic *api.Restic) {
	sel, err := metav1.LabelSelectorAsSelector(&restic.Spec.Selector)
	if err != nil {
		ref, rerr := reference.GetReference(scheme.Scheme, restic)
		if rerr == nil {
			c.recorder.Eventf(
				ref,
				core.EventTypeWarning,
				eventer.EventReasonInvalidRestic,
				"Reason: %s",
				err.Error(),
			)
		}
		return
	}
	{
		if resources, err := c.dpLister.Deployments(restic.Namespace).List(sel); err == nil {
			for _, resource := range resources {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.dpQueue.GetQueue().Add(key)
				}
			}
		}
	}
	{
		if resources, err := c.dsLister.DaemonSets(restic.Namespace).List(sel); err == nil {
			for _, resource := range resources {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.dsQueue.GetQueue().Add(key)
				}
			}
		}
	}
	{
		if resources, err := c.ssLister.StatefulSets(restic.Namespace).List(sel); err == nil {
			for _, resource := range resources {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.ssQueue.GetQueue().Add(key)
				}
			}
		}
	}
	{
		if resources, err := c.rcLister.ReplicationControllers(restic.Namespace).List(sel); err == nil {
			for _, resource := range resources {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.rcQueue.GetQueue().Add(key)
				}
			}
		}
	}
	{
		if resources, err := c.rsLister.ReplicaSets(restic.Namespace).List(sel); err == nil {
			for _, resource := range resources {
				// If owned by a Deployment, skip it.
				// OCFIX
				if apps_util.IsOwnedByDeployment(resource.OwnerReferences) {
					continue
				}
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.rsQueue.GetQueue().Add(key)
				}
			}
		}
	}
}

func (c *StashController) EnsureSidecarDeleted(namespace, name string) {
	if resources, err := c.dpLister.Deployments(namespace).List(labels.Everything()); err == nil {
		for _, resource := range resources {
			restic, err := util.GetAppliedRestic(resource.Annotations)
			if err != nil {
				if ref, e2 := reference.GetReference(scheme.Scheme, resource); e2 == nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason: %s",
						err.Error(),
					)
				}
			} else if restic != nil && restic.Namespace == namespace && restic.Name == name {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.dpQueue.GetQueue().Add(key)
				}
			}
		}
	}
	if resources, err := c.dsLister.DaemonSets(namespace).List(labels.Everything()); err == nil {
		for _, resource := range resources {
			restic, err := util.GetAppliedRestic(resource.Annotations)
			if err != nil {
				if ref, e2 := reference.GetReference(scheme.Scheme, resource); e2 == nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason: %s",
						err.Error(),
					)
				}
			} else if restic != nil && restic.Namespace == namespace && restic.Name == name {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.dsQueue.GetQueue().Add(key)
				}
			}
		}
	}
	if resources, err := c.ssLister.StatefulSets(namespace).List(labels.Everything()); err == nil {
		for _, resource := range resources {
			restic, err := util.GetAppliedRestic(resource.Annotations)
			if err != nil {
				if ref, e2 := reference.GetReference(scheme.Scheme, resource); e2 != nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason: %s",
						err.Error(),
					)
				}
			} else if restic != nil && restic.Namespace == namespace && restic.Name == name {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.ssQueue.GetQueue().Add(key)
				}
			}
		}
	}
	if resources, err := c.rcLister.ReplicationControllers(namespace).List(labels.Everything()); err == nil {
		for _, resource := range resources {
			restic, err := util.GetAppliedRestic(resource.Annotations)
			if err != nil {
				if ref, e2 := reference.GetReference(scheme.Scheme, resource); e2 == nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason: %s",
						err.Error(),
					)
				}
			} else if restic != nil && restic.Namespace == namespace && restic.Name == name {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.rcQueue.GetQueue().Add(key)
				}
			}
		}
	}
	if resources, err := c.rsLister.ReplicaSets(namespace).List(labels.Everything()); err == nil {
		for _, resource := range resources {
			restic, err := util.GetAppliedRestic(resource.Annotations)
			if err != nil {
				if ref, e2 := reference.GetReference(scheme.Scheme, resource); e2 == nil {
					c.recorder.Eventf(
						ref,
						core.EventTypeWarning,
						eventer.EventReasonInvalidRestic,
						"Reason: %s",
						err.Error(),
					)
				}
			} else if restic != nil && restic.Namespace == namespace && restic.Name == name {
				key, err := cache.MetaNamespaceKeyFunc(resource)
				if err == nil {
					c.rsQueue.GetQueue().Add(key)
				}
			}
		}
	}
}
