/*
Copyright 2021 Syntasso.

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

package controllers

import (
	"context"
	"path/filepath"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	Client    client.Client
	Log       logr.Logger
	Scheduler *Scheduler
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=clusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"cluster", req.NamespacedName,
	)

	cluster := &platformv1alpha1.Cluster{}
	logger.Info("Registering Cluster", "requestName", req.Name)
	if err := r.Client.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	stateStore := &platformv1alpha1.StateStore{}
	stateStoreRef := types.NamespacedName{
		Name:      cluster.Spec.StateStoreRef.Name,
		Namespace: or(cluster.Spec.StateStoreRef.Namespace, cluster.Namespace),
	}
	if err := r.Client.Get(ctx, stateStoreRef, stateStore); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "not found", "stateStoreRef", stateStoreRef)
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.Spec.SecretRef.Name,
		Namespace: or(stateStore.Spec.SecretRef.Namespace, stateStore.Namespace),
	}
	if err := r.Client.Get(ctx, secretRef, secret); err != nil {
		if errors.IsNotFound(err) {
			logger.Error(err, "not found", "secretRef", secretRef)
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	stateStore.SetCredentials(secret)

	writer, err := writers.NewStateStoreWriter(
		logger.WithName("writers").WithName("StateStoreWriter"),
		stateStore,
	)
	if err != nil {
		logger.Error(err, "unable to create StateStoreWriter")
		return ctrl.Result{}, err
	}

	path := filepath.Join(cluster.Spec.Path, cluster.Namespace, cluster.Name)
	logger = logger.WithValues("path", path)

	if err := r.createCrdPathWithExample(writer, path); err != nil {
		logger.Error(err, "unable to write worker cluster resources to bucket")
		return defaultRequeue, nil
	}

	if err := r.createResourcePathWithExample(writer, path); err != nil {
		logger.Error(err, "unable to write worker resources to bucket")
		return defaultRequeue, nil
	}

	if err := r.Scheduler.ReconcileCluster(); err != nil {
		logger.Error(err, "unable to schedule cluster resources")
		return defaultRequeue, nil
	}
	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) createResourcePathWithExample(writer writers.StateStoreWriter, bucketPath string) error {
	path := filepath.Join(bucketPath, "resources")
	kratixConfigMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kratix-info",
			Namespace: "kratix-worker-system",
		},
		Data: map[string]string{
			"Path": path,
		},
	}
	nsBytes, _ := yaml.Marshal(kratixConfigMap)

	return writer.WriteObject(path, "kratix-resources.yaml", nsBytes)
}

func (r *ClusterReconciler) createCrdPathWithExample(writer writers.StateStoreWriter, bucketPath string) error {
	kratixNamespace := &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
	}
	nsBytes, _ := yaml.Marshal(kratixNamespace)

	path := filepath.Join(bucketPath, "crds")
	return writer.WriteObject(path, "kratix-crds.yaml", nsBytes)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Cluster{}).
		Complete(r)
}
