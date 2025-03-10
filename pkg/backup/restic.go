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

package backup

import (
	api "stash.appscode.dev/apimachinery/apis/stash/v1alpha1"
	"stash.appscode.dev/stash/pkg/eventer"
	"stash.appscode.dev/stash/pkg/util"

	"github.com/appscode/go/log"
	"github.com/golang/glog"
	core "k8s.io/api/core/v1"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/reference"
	"kmodules.xyz/client-go/tools/queue"
)

func (c *Controller) initResticWatcher() {
	// TODO: Watch one Restic object, when support for Kubernetes 1.8 is dropped.
	// ref: https://github.com/kubernetes/kubernetes/pull/53345

	c.rInformer = c.stashInformerFactory.Stash().V1alpha1().Restics().Informer()
	c.rQueue = queue.New("Restic", c.opt.MaxNumRequeues, c.opt.NumThreads, c.runResticScheduler)
	c.rInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if r, ok := obj.(*api.Restic); ok && r.Name == c.opt.ResticName && r.IsValid() == nil {
				queue.Enqueue(c.rQueue.GetQueue(), r)
			}
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			old := oldObj.(*api.Restic)
			nu := newObj.(*api.Restic)
			if !util.ResticEqual(old, nu) && nu.Name == c.opt.ResticName && nu.IsValid() == nil {
				queue.Enqueue(c.rQueue.GetQueue(), nu)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// IndexerInformer uses a delta queue, therefore for deletes we have to use this
			// key function.
			if r, ok := obj.(*api.Restic); ok && r.Name == c.opt.ResticName {
				queue.Enqueue(c.rQueue.GetQueue(), obj)
			}
		},
	})
	c.rLister = c.stashInformerFactory.Stash().V1alpha1().Restics().Lister()
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the deployment to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *Controller) runResticScheduler(key string) error {
	obj, exists, err := c.rInformer.GetIndexer().GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with a Restic, so that we will see a delete for one d
		glog.Warningf("Restic %s does not exist anymore\n", key)

		c.cron.Stop()
	} else {
		r := obj.(*api.Restic)
		glog.Infof("Sync/Add/Update for Restic %s", r.GetName())

		err := c.configureScheduler(r)
		if err != nil {
			ref, rerr := reference.GetReference(clientsetscheme.Scheme, r)
			if rerr == nil {
				c.recorder.Eventf(
					ref,
					core.EventTypeWarning,
					eventer.EventReasonFailedToBackup,
					"Failed to start Stash scheduler reason %v",
					err,
				)
			} else {
				log.Errorf("Failed to write event on %s %s. Reason: %s", r.Kind, r.Name, rerr)
			}
			log.Errorln(err)
		}
	}
	return nil
}
