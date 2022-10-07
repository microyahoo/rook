/*
Copyright 2016 The Rook Authors. All rights reserved.

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

package object

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/ceph/go-ceph/rgw/admin"
	"github.com/coreos/pkg/capnslog"
	bktclient "github.com/kube-object-storage/lib-bucket-provisioner/pkg/client/clientset/versioned"
	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/config"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/operator/ceph/reporting"
	"github.com/rook/rook/pkg/operator/k8sutil"
	"github.com/rook/rook/pkg/util/exec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "ceph-object-controller"
)

var waitForRequeueIfObjectStoreNotReady = reconcile.Result{Requeue: true, RequeueAfter: 10 * time.Second}

var logger = capnslog.NewPackageLogger("github.com/rook/rook", controllerName)

// List of object resources to watch by the controller
var objectsToWatch = []client.Object{
	&corev1.Secret{TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: corev1.SchemeGroupVersion.String()}},
	&corev1.Service{TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: corev1.SchemeGroupVersion.String()}},
	&appsv1.Deployment{TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: appsv1.SchemeGroupVersion.String()}},
}

var cephObjectStoreKind = reflect.TypeOf(cephv1.CephObjectStore{}).Name()

// Sets the type meta for the controller main object
var controllerTypeMeta = metav1.TypeMeta{
	Kind:       cephObjectStoreKind,
	APIVersion: fmt.Sprintf("%s/%s", cephv1.CustomResourceGroup, cephv1.Version),
}

var currentAndDesiredCephVersion = opcontroller.CurrentAndDesiredCephVersion

// allow this to be overridden for unit tests
var cephObjectStoreDependents = CephObjectStoreDependents

// ReconcileCephObjectStore reconciles a cephObjectStore object
type ReconcileCephObjectStore struct {
	client           client.Client
	bktclient        bktclient.Interface
	scheme           *runtime.Scheme
	context          *clusterd.Context
	clusterSpec      *cephv1.ClusterSpec
	clusterInfo      *cephclient.ClusterInfo
	recorder         record.EventRecorder
	opManagerContext context.Context
	opConfig         opcontroller.OperatorConfig
}

// Add creates a new cephObjectStore Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context, opConfig opcontroller.OperatorConfig) error {
	return add(mgr, newReconciler(mgr, context, opManagerContext, opConfig))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context, opConfig opcontroller.OperatorConfig) reconcile.Reconciler {
	context.Client = mgr.GetClient()
	return &ReconcileCephObjectStore{
		client:           mgr.GetClient(),
		scheme:           mgr.GetScheme(),
		context:          context,
		bktclient:        bktclient.NewForConfigOrDie(context.KubeConfig),
		recorder:         mgr.GetEventRecorderFor("rook-" + controllerName),
		opManagerContext: opManagerContext,
		opConfig:         opConfig,
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	logger.Info("successfully started")

	// Watch for changes on the cephObjectStore CRD object
	err = c.Watch(&source.Kind{Type: &cephv1.CephObjectStore{TypeMeta: controllerTypeMeta}}, &handler.EnqueueRequestForObject{}, opcontroller.WatchControllerPredicate())
	if err != nil {
		return err
	}

	// Watch all other resources
	for _, t := range objectsToWatch {
		err = c.Watch(&source.Kind{Type: t}, &handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &cephv1.CephObjectStore{},
		}, opcontroller.WatchPredicateForNonCRDObject(&cephv1.CephObjectStore{TypeMeta: controllerTypeMeta}, mgr.GetScheme()))
		if err != nil {
			return err
		}
	}

	return nil
}

// Reconcile reads that state of the cluster for a cephObjectStore object and makes changes based on the state read
// and what is in the cephObjectStore.Spec
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileCephObjectStore) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	// workaround because the rook logging mechanism is not compatible with the controller-runtime logging interface
	reconcileResponse, objectStore, err := r.reconcile(request)

	return reporting.ReportReconcileResult(logger, r.recorder, request,
		&objectStore, reconcileResponse, err)
}

func (r *ReconcileCephObjectStore) reconcile(request reconcile.Request) (reconcile.Result, cephv1.CephObjectStore, error) {
	// Fetch the cephObjectStore instance
	cephObjectStore := &cephv1.CephObjectStore{}
	err := r.client.Get(r.opManagerContext, request.NamespacedName, cephObjectStore)
	if err != nil {
		if kerrors.IsNotFound(err) {
			logger.Debug("cephObjectStore resource not found. Ignoring since object must be deleted.")
			return reconcile.Result{}, *cephObjectStore, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to get cephObjectStore")
	}
	// update observedGeneration local variable with current generation value,
	// because generation can be changed before reconcile got completed
	// CR status will be updated at end of reconcile, so to reflect the reconcile has finished
	observedGeneration := cephObjectStore.ObjectMeta.Generation

	// Set a finalizer so we can do cleanup before the object goes away
	err = opcontroller.AddFinalizerIfNotPresent(r.opManagerContext, r.client, cephObjectStore)
	if err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to add finalizer")
	}

	// The CR was just created, initializing status fields
	if cephObjectStore.Status == nil {
		// The store is not available so let's not build the status Info yet
		updateStatus(r.opManagerContext, k8sutil.ObservedGenerationNotAvailable, r.client, request.NamespacedName, cephv1.ConditionProgressing, map[string]string{})
	} else {
		updateStatus(r.opManagerContext, k8sutil.ObservedGenerationNotAvailable, r.client, request.NamespacedName, cephv1.ConditionProgressing, buildStatusInfo(cephObjectStore))
	}

	// Make sure a CephCluster is present otherwise do nothing
	cephCluster, isReadyToReconcile, cephClusterExists, reconcileResponse := opcontroller.IsReadyToReconcile(r.opManagerContext, r.client, request.NamespacedName, controllerName)
	if !isReadyToReconcile {
		// This handles the case where the Ceph Cluster is gone and we want to delete that CR
		// We skip the deleteStore() function since everything is gone already
		//
		// Also, only remove the finalizer if the CephCluster is gone
		// If not, we should wait for it to be ready
		// This handles the case where the operator is not ready to accept Ceph command but the cluster exists
		if !cephObjectStore.GetDeletionTimestamp().IsZero() && !cephClusterExists {
			// Remove finalizer
			err := opcontroller.RemoveFinalizer(r.opManagerContext, r.client, cephObjectStore)
			if err != nil {
				return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to remove finalizer")
			}

			// Return and do not requeue. Successful deletion.
			return reconcile.Result{}, *cephObjectStore, nil
		}

		return reconcileResponse, *cephObjectStore, nil
	}
	r.clusterSpec = &cephCluster.Spec

	// Populate clusterInfo during each reconcile
	r.clusterInfo, _, _, err = opcontroller.LoadClusterInfo(r.context, r.opManagerContext, request.NamespacedName.Namespace)
	if err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to populate cluster info")
	}

	// DELETE: the CR was deleted
	if !cephObjectStore.GetDeletionTimestamp().IsZero() {
		updateStatus(r.opManagerContext, k8sutil.ObservedGenerationNotAvailable, r.client, request.NamespacedName, cephv1.ConditionDeleting, buildStatusInfo(cephObjectStore))

		// Detect running Ceph version
		runningCephVersion, err := cephclient.LeastUptodateDaemonVersion(r.context, r.clusterInfo, config.MonType)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to retrieve current ceph %q version", config.MonType)
		}
		r.clusterInfo.CephVersion = runningCephVersion
		r.clusterInfo.Context = r.opManagerContext

		// get the latest version of the object to check dependencies
		err = r.client.Get(r.opManagerContext, request.NamespacedName, cephObjectStore)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to get latest CephObjectStore %q", request.NamespacedName.String())
		}
		objCtx, err := NewMultisiteContext(r.context, r.clusterInfo, cephObjectStore)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to get object context")
		}
		opsCtx, err := NewMultisiteAdminOpsContext(objCtx, &cephObjectStore.Spec)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to get admin ops API context")
		}

		// ignore any errors coming from this for the deletion case since we could get into a
		// partially-deleted case where RGWs aren't responding. Deletion will be blocked later if
		// the bucket fails to be deleted and the RGWs are running.
		_ = removeDeprecatedHealthCheckBucket(r.clusterInfo.Context, opsCtx, cephObjectStore)

		deps, err := cephObjectStoreDependents(r.context, r.clusterInfo, cephObjectStore, objCtx, opsCtx)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, err
		}
		if !deps.Empty() {
			err := reporting.ReportDeletionBlockedDueToDependents(r.opManagerContext, logger, r.client, cephObjectStore, deps)
			return opcontroller.WaitForRequeueIfFinalizerBlocked, *cephObjectStore, err
		}
		reporting.ReportDeletionNotBlockedDueToDependents(r.opManagerContext, logger, r.client, r.recorder, cephObjectStore)

		cfg := clusterConfig{
			context:     r.context,
			store:       cephObjectStore,
			clusterSpec: r.clusterSpec,
			clusterInfo: r.clusterInfo,
		}
		cfg.deleteStore()

		// Remove finalizer
		err = opcontroller.RemoveFinalizer(r.opManagerContext, r.client, cephObjectStore)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to remove finalizer")
		}

		// Return and do not requeue. Successful deletion.
		return reconcile.Result{}, *cephObjectStore, nil
	}

	if cephObjectStore.Spec.IsExternal() {
		// Check the ceph version of the running monitors
		desiredCephVersion, err := cephclient.LeastUptodateDaemonVersion(r.context, r.clusterInfo, config.MonType)
		if err != nil {
			return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to retrieve current ceph %q version", config.MonType)
		}
		r.clusterInfo.CephVersion = desiredCephVersion
	} else {
		// Detect desired CephCluster version
		runningCephVersion, desiredCephVersion, err := currentAndDesiredCephVersion(
			r.opManagerContext,
			r.opConfig.Image,
			cephObjectStore.Namespace,
			controllerName,
			k8sutil.NewOwnerInfo(cephObjectStore, r.scheme),
			r.context,
			r.clusterSpec,
			r.clusterInfo,
		)
		if err != nil {
			if strings.Contains(err.Error(), opcontroller.UninitializedCephConfigError) {
				logger.Info(opcontroller.OperatorNotInitializedMessage)
				return opcontroller.WaitForRequeueIfOperatorNotInitialized, *cephObjectStore, nil
			}
			return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "failed to detect running and desired ceph version")
		}

		// If the version of the Ceph monitor differs from the CephCluster CR image version we assume
		// the cluster is being upgraded. So the controller will just wait for the upgrade to finish and
		// then versions should match. Obviously using the cmd reporter job adds up to the deployment time
		if !reflect.DeepEqual(*runningCephVersion, *desiredCephVersion) {
			// Upgrade is in progress, let's wait for the mons to be done
			return opcontroller.WaitForRequeueIfCephClusterIsUpgrading,
				*cephObjectStore,
				opcontroller.ErrorCephUpgradingRequeue(desiredCephVersion, runningCephVersion)
		}
		r.clusterInfo.CephVersion = *desiredCephVersion
	}

	// validate the store settings
	if err := r.validateStore(cephObjectStore); err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "invalid object store %q arguments", cephObjectStore.Name)
	}

	// CREATE/UPDATE
	_, err = r.reconcileCreateObjectStore(cephObjectStore, request.NamespacedName, cephCluster.Spec)
	if err != nil && kerrors.IsNotFound(err) {
		logger.Info(opcontroller.OperatorNotInitializedMessage)
		return opcontroller.WaitForRequeueIfOperatorNotInitialized, *cephObjectStore, nil
	} else if err != nil {
		result, err := r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, request.NamespacedName, "failed to create object store deployments", err)
		return result, *cephObjectStore, err
	}

	objCtx, err := NewMultisiteContext(r.context, r.clusterInfo, cephObjectStore)
	if err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to get object context")
	}
	opsCtx, err := NewMultisiteAdminOpsContext(objCtx, &cephObjectStore.Spec)
	if err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrapf(err, "failed to get admin ops API context")
	}
	err = removeDeprecatedHealthCheckBucket(r.clusterInfo.Context, opsCtx, cephObjectStore)
	if err != nil {
		return reconcile.Result{}, *cephObjectStore, errors.Wrap(err, "updated object store but failed to remove deprecated health check bucket")
	}

	// update ObservedGeneration in status at the end of reconcile
	// Set Progressing status, we are done reconciling, the health check go routine will update the status
	updateStatus(r.opManagerContext, observedGeneration, r.client, request.NamespacedName, cephv1.ConditionReady, buildStatusInfo(cephObjectStore))

	// Return and do not requeue
	logger.Debug("done reconciling")
	return reconcile.Result{}, *cephObjectStore, nil
}

func (r *ReconcileCephObjectStore) reconcileCreateObjectStore(cephObjectStore *cephv1.CephObjectStore, namespacedName types.NamespacedName, cluster cephv1.ClusterSpec) (reconcile.Result, error) {
	ownerInfo := k8sutil.NewOwnerInfo(cephObjectStore, r.scheme)
	cfg := clusterConfig{
		context:     r.context,
		clusterInfo: r.clusterInfo,
		store:       cephObjectStore,
		rookVersion: r.clusterSpec.CephVersion.Image,
		clusterSpec: r.clusterSpec,
		DataPathMap: config.NewStatelessDaemonDataPathMap(config.RgwType, cephObjectStore.Name, cephObjectStore.Namespace, r.clusterSpec.DataDirHostPath),
		client:      r.client,
		ownerInfo:   ownerInfo,
	}
	objContext, err := NewMultisiteContext(r.context, r.clusterInfo, cephObjectStore)
	if err != nil {
		return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to setup object store context", err)
	}
	objContext.CephClusterSpec = cluster

	if cephObjectStore.Spec.IsExternal() {
		logger.Info("reconciling external object store")

		// Before v1.11, Rook created a Service and custom Endpoints that routed to external RGW
		// endpoints. This causes problems if the external endpoint has TLS certificates that block
		// connections to other endpoints. This also makes it impossible to create an external mode
		// CephObjectStore that references another CephObjectStore's Service endpoint in the same
		// Kubernetes cluster.
		//
		// TODO: this code block can be removed once OBCs are no longer supported. The legacy
		// service can also be removed at that point.
		service := cfg.generateService(cephObjectStore)
		clientset := cfg.context.Clientset
		clusterCtx := cfg.clusterInfo.Context
		_, err = clientset.CoreV1().Services(service.Namespace).Get(clusterCtx, service.Name, metav1.GetOptions{})
		if err != nil {
			if kerrors.IsNotFound(err) {
				// We do not need to create Services/Endpoints for new CephObjectStores.
			} else {
				return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName,
					"failed to determine if legacy external service exists", err)
			}
		} else {
			// For any legacy users that have an external mode CephObjectStore successfully using
			// the Service/Endpoints and who have already created OBCs,we  leave the legacy
			// Service/Endpoints in place. We need to update legacy services if the user edits the
			// externalRgwEndpoint -- perhaps their RGW node changed IPs.

			// RECONCILE SERVICE
			logger.Info("reconciling legacy external object store service")
			err = cfg.reconcileService(cephObjectStore)
			if err != nil {
				return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to reconcile service", err)
			}

			// RECONCILE ENDPOINTS
			// Always add the endpoint AFTER the service otherwise it will get overridden
			logger.Info("reconciling legacy external object store endpoint")
			err = cfg.reconcileExternalEndpoint(cephObjectStore)
			if err != nil {
				return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to reconcile external endpoint", err)
			}
		}

		if err := UpdateEndpoint(objContext, cephObjectStore); err != nil {
			return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to set endpoint", err)
		}
	} else {
		logger.Info("reconciling object store deployments")

		// Reconcile realm/zonegroup/zone CRs & update their names
		realmName, zoneGroupName, zoneName, zone, reconcileResponse, err := r.reconcileMultisiteCRs(cephObjectStore)
		if err != nil {
			return reconcileResponse, err
		}

		// Reconcile Ceph Zone if Multisite
		if cephObjectStore.Spec.IsMultisite() {
			reconcileResponse, err := r.reconcileCephZone(cephObjectStore, zoneGroupName, realmName)
			if err != nil {
				return reconcileResponse, err
			}
		}

		objContext.Realm = realmName
		objContext.ZoneGroup = zoneGroupName
		objContext.Zone = zoneName
		logger.Debugf("realm for object-store is %q, zone group for object-store is %q, zone for object-store is %q", objContext.Realm, objContext.ZoneGroup, objContext.Zone)

		// RECONCILE SERVICE
		logger.Debug("reconciling object store service")
		err = cfg.reconcileService(cephObjectStore)
		if err != nil {
			return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to reconcile service", err)
		}

		if err := UpdateEndpoint(objContext, cephObjectStore); err != nil {
			return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to set endpoint", err)
		}

		// Reconcile Pool Creation
		if !cephObjectStore.Spec.IsMultisite() {
			logger.Info("reconciling object store pools")
			err = CreatePools(objContext, r.clusterSpec, cephObjectStore.Spec.MetadataPool, cephObjectStore.Spec.DataPool)
			if err != nil {
				return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to create object pools", err)
			}
		}

		// Reconcile Multisite Creation
		logger.Infof("setting multisite settings for object store %q", cephObjectStore.Name)
		err = setMultisite(objContext, cephObjectStore, zone)
		if err != nil && kerrors.IsNotFound(err) {
			return reconcile.Result{}, err
		} else if err != nil {
			return r.setFailedStatus(k8sutil.ObservedGenerationNotAvailable, namespacedName, "failed to configure multisite for object store", err)
		}

		// Create or Update Store
		err = cfg.createOrUpdateStore(realmName, zoneGroupName, zoneName)
		if err != nil {
			return reconcile.Result{}, errors.Wrapf(err, "failed to create object store %q", cephObjectStore.Name)
		}
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileCephObjectStore) reconcileCephZone(store *cephv1.CephObjectStore, zoneGroupName string, realmName string) (reconcile.Result, error) {
	realmArg := fmt.Sprintf("--rgw-realm=%s", realmName)
	zoneGroupArg := fmt.Sprintf("--rgw-zonegroup=%s", zoneGroupName)
	zoneArg := fmt.Sprintf("--rgw-zone=%s", store.Spec.Zone.Name)
	objContext := NewContext(r.context, r.clusterInfo, store.Name)

	_, err := RunAdminCommandNoMultisite(objContext, true, "zone", "get", realmArg, zoneGroupArg, zoneArg)
	if err != nil {
		// ENOENT mean "No such file or directory"
		if code, err := exec.ExtractExitCode(err); err == nil && code == int(syscall.ENOENT) {
			return waitForRequeueIfObjectStoreNotReady, errors.Wrapf(err, "ceph zone %q not found", store.Spec.Zone.Name)
		} else {
			return waitForRequeueIfObjectStoreNotReady, errors.Wrapf(err, "radosgw-admin zone get failed with code %d", code)
		}
	}

	logger.Infof("Zone %q found in Ceph cluster will include object store %q", store.Spec.Zone.Name, store.Name)
	return reconcile.Result{}, nil
}

func (r *ReconcileCephObjectStore) reconcileMultisiteCRs(cephObjectStore *cephv1.CephObjectStore) (string, string, string, *cephv1.CephObjectZone, reconcile.Result, error) {
	if cephObjectStore.Spec.IsMultisite() {
		zoneName := cephObjectStore.Spec.Zone.Name
		zone := &cephv1.CephObjectZone{}
		err := r.client.Get(r.opManagerContext, types.NamespacedName{Name: zoneName, Namespace: cephObjectStore.Namespace}, zone)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, err
			}
			return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, errors.Wrapf(err, "error getting CephObjectZone %q", cephObjectStore.Spec.Zone.Name)
		}
		logger.Debugf("CephObjectZone resource %s found", zone.Name)

		zonegroup := &cephv1.CephObjectZoneGroup{}
		err = r.client.Get(r.opManagerContext, types.NamespacedName{Name: zone.Spec.ZoneGroup, Namespace: cephObjectStore.Namespace}, zonegroup)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, err
			}
			return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, errors.Wrapf(err, "error getting CephObjectZoneGroup %q", zone.Spec.ZoneGroup)
		}
		logger.Debugf("CephObjectZoneGroup resource %s found", zonegroup.Name)

		realm := &cephv1.CephObjectRealm{}
		err = r.client.Get(r.opManagerContext, types.NamespacedName{Name: zonegroup.Spec.Realm, Namespace: cephObjectStore.Namespace}, realm)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, err
			}
			return "", "", "", nil, waitForRequeueIfObjectStoreNotReady, errors.Wrapf(err, "error getting CephObjectRealm %q", zonegroup.Spec.Realm)
		}
		logger.Debugf("CephObjectRealm resource %s found", realm.Name)

		return realm.Name, zonegroup.Name, zone.Name, zone, reconcile.Result{}, nil
	}

	return cephObjectStore.Name, cephObjectStore.Name, cephObjectStore.Name, nil, reconcile.Result{}, nil
}

// handle upgrade from v1.10; we need to remove the health checker bucket from the store, or
// deleting the store will be stuck on dependents because an unknown bucket exists
// TODO: remove this for Rook v1.12 release
var removeDeprecatedHealthCheckBucket = func(ctx context.Context, opsCtx *AdminOpsContext, cos *cephv1.CephObjectStore) error {
	healthCheckBucket := genHealthCheckerBucketName(string(cos.UID))
	doPurge := true // purge all content of the bucket
	bucket := admin.Bucket{Bucket: healthCheckBucket, PurgeObject: &doPurge}
	remErr := opsCtx.AdminOpsClient.RemoveBucket(ctx, bucket)

	if remErr == nil {
		logger.Info("successfully deleted deprecated health checker bucket")
		return nil
	}

	if errors.Is(remErr, admin.ErrNoSuchBucket) {
		logger.Debug("deprecated health checker bucket already does not exist: NoSuchBucket")
		return nil
	}

	if errors.Is(remErr, admin.ErrNoSuchKey) {
		// ceph might return NoSuchKey than NoSuchBucket when the target bucket does not exist.
		// then we can use GetBucketInfo() to judge the existence of the bucket.
		// see: https://github.com/ceph/ceph/pull/44413
		_, getErr := opsCtx.AdminOpsClient.GetBucketInfo(ctx, bucket)
		if getErr != nil {
			if errors.Is(getErr, admin.ErrNoSuchBucket) {
				logger.Debug("deprecated health checker bucket already does not exist: NoSuchKey")
				return nil
			}
			// both commands errored; something is up; be sure to return both errors
			return errors.Wrapf(remErr, "failed to delete deprecated health checker bucket %q. failed to get bucket info. %v", healthCheckBucket, getErr)
		}
		// remove bucket failed, and bucket exists; something else went wrong with remove bucket
	}

	return errors.Wrapf(remErr, "failed to delete deprecated health checker bucket %q", healthCheckBucket)
}
