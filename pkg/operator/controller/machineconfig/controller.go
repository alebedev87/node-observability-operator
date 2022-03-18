/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package machineconfigcontroller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	mcv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	v1alpha1 "github.com/openshift/node-observability-operator/api/v1alpha1"
)

// MachineconfigReconciler reconciles a Machineconfig object
type MachineconfigReconciler struct {
	client.Client

	Scheme     *runtime.Scheme
	Log        logr.Logger
	CtrlConfig *v1alpha1.Machineconfig
}

//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=machineconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=machineconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=machineconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Machineconfig object Gagainst the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *MachineconfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("nodeobservability/machineconfig", req.NamespacedName)
	r.Log.Info("Reconciling MachineConfig of Nodeobservability operator")

	// Fetch the nodeobservability.olm.openshift.io/machineconfig CR
	r.CtrlConfig = &v1alpha1.Machineconfig{}
	err := r.Get(ctx, req.NamespacedName, r.CtrlConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			r.Log.Info("MachineConfig resource not found. Ignoring could have been deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.Log.Error(err, "failed to fetch MachineConfig")
		return ctrl.Result{Requeue: true, RequeueAfter: 15 * time.Second}, err
	}
	r.Log.Info(fmt.Sprintf("MachineConfig resource found : Namespace %s : Name %s ", req.NamespacedName.Namespace, req.NamespacedName.Name))

	_, justCreated, err := r.ensureCrioProfConfigExists(ctx)
	if err != nil {
		r.Log.Error(err, "failed to fetch crio profiling config")
		return ctrl.Result{Requeue: true, RequeueAfter: 15 * time.Second}, err
	}

	if justCreated {
		return r.checkMCPUpdateStatus(ctx, req)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MachineconfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Machineconfig{}).
		Complete(r)
}

// checkMCPUpdateStatus is for reconciling update status of all worker machines
func (r *MachineconfigReconciler) checkMCPUpdateStatus(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	listOptions := []client.ListOption{}
	mcpList := &mcv1.MachineConfigPoolList{}
	err := r.Client.List(context.TODO(), mcpList, listOptions...)
	if err != nil {
		r.Log.Error(err, "failed to fetch list of MCPs")
		return ctrl.Result{}, err
	}

	var mcp mcv1.MachineConfigPool
	for _, mcp = range mcpList.Items {
		if mcp.Name == "worker" {
			break
		}
	}

	if mcv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcv1.MachineConfigPoolUpdating) &&
		r.CtrlConfig.Status.UpdateStatus.InProgress == "false" {
		r.Log.Info("config update under progress")
		r.CtrlConfig.Status.UpdateStatus.InProgress = corev1.ConditionTrue
		return ctrl.Result{Requeue: true, RequeueAfter: 15 * time.Second}, nil
	}

	if mcv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcv1.MachineConfigPoolUpdated) &&
		mcp.Status.UpdatedMachineCount == mcp.Status.MachineCount {
		r.Log.Info("config update completed on all machines")
		r.CtrlConfig.Status.UpdateStatus.InProgress = "false"
	} else {
		r.Log.Info("waiting for update to finish on all machines", "MachineConfigPool", "worker")
		return ctrl.Result{Requeue: true, RequeueAfter: 15 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}
