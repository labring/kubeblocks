/*
Copyright (C) 2022-2025 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package operations

import (
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "github.com/apecloud/kubeblocks/apis/apps/v1"
	opsv1alpha1 "github.com/apecloud/kubeblocks/apis/operations/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	intctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

type StartOpsHandler struct{}

var _ OpsHandler = StartOpsHandler{}

func init() {
	startBehaviour := OpsBehaviour{
		FromClusterPhases: append(appsv1.GetClusterUpRunningPhases(), appsv1.UpdatingClusterPhase,
			appsv1.StoppedClusterPhase, appsv1.StoppingClusterPhase),
		ToClusterPhase: appsv1.UpdatingClusterPhase,
		QueueByCluster: true,
		OpsHandler:     StartOpsHandler{},
	}

	opsMgr := GetOpsManager()
	opsMgr.RegisterOps(opsv1alpha1.StartType, startBehaviour)
}

// ActionStartedCondition the started condition when handling the start request.
func (start StartOpsHandler) ActionStartedCondition(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) (*metav1.Condition, error) {
	return opsv1alpha1.NewStartCondition(opsRes.OpsRequest), nil
}

// Action modifies Cluster.spec.components[*].replicas from the opsRequest
func (start StartOpsHandler) Action(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) error {
	var (
		cluster   = opsRes.Cluster
		startList = opsRes.OpsRequest.Spec.StartList
	)
	compOpsHelper := newComponentOpsHelper(startList)
	// abort earlier running opsRequests.
	if err := abortEarlierOpsRequestWithSameKind(reqCtx, cli, opsRes, []opsv1alpha1.OpsType{opsv1alpha1.StopType},
		func(earlierOps *opsv1alpha1.OpsRequest) (bool, error) {
			if len(startList) == 0 {
				// start all components
				return true, nil
			}
			return len(earlierOps.Spec.StopList) == 0 || hasIntersectionCompOpsList(compOpsHelper.componentOpsSet, earlierOps.Spec.StopList), nil
		}); err != nil {
		return err
	}
	startComp := func(compSpec *appsv1.ClusterComponentSpec, clusterCompName string) {
		if len(startList) > 0 {
			if _, ok := compOpsHelper.componentOpsSet[clusterCompName]; !ok {
				return
			}
		}
		compSpec.Stop = nil
		// Backward compatibility: older stop flows persisted stopped components as replicas=0.
		if compSpec.Replicas == 0 {
			restoreComponentReplicas(cluster, compSpec)
		}
	}
	for i, v := range cluster.Spec.ComponentSpecs {
		startComp(&cluster.Spec.ComponentSpecs[i], v.Name)
	}
	for i, v := range cluster.Spec.Shardings {
		startComp(&cluster.Spec.Shardings[i].Template, v.Name)
	}
	if cluster.Annotations != nil {
		delete(cluster.Annotations, constant.SnapShotForStartAnnotationKey)
	}
	return cli.Update(reqCtx.Ctx, cluster)
}

// ReconcileAction will be performed when action is done and loops till OpsRequest.status.phase is Succeed/Failed.
// the Reconcile function for start opsRequest.
func (start StartOpsHandler) ReconcileAction(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) (opsv1alpha1.OpsPhase, time.Duration, error) {
	handleComponentProgress := func(reqCtx intctrlutil.RequestCtx,
		cli client.Client,
		opsRes *OpsResource,
		pgRes *progressResource,
		compStatus *opsv1alpha1.OpsRequestComponentStatus) (int32, int32, error) {
		var err error
		pgRes.createdPodSet, err = intctrlcomp.GenerateAllPodNamesToSet(pgRes.clusterComponent.Replicas, pgRes.clusterComponent.Instances,
			pgRes.clusterComponent.OfflineInstances, opsRes.Cluster.Name, pgRes.fullComponentName)
		if err != nil {
			return 0, 0, err
		}
		return handleComponentProgressForScalingReplicas(reqCtx, cli, opsRes, pgRes, compStatus)
	}
	compOpsHelper := newComponentOpsHelper(opsRes.OpsRequest.Spec.StartList)
	return compOpsHelper.reconcileActionWithComponentOps(reqCtx, cli, opsRes, "start", handleComponentProgress)
}

// SaveLastConfiguration records last configuration to the OpsRequest.status.lastConfiguration
func (start StartOpsHandler) SaveLastConfiguration(reqCtx intctrlutil.RequestCtx, cli client.Client, opsRes *OpsResource) error {
	return nil
}

func restoreComponentReplicas(cluster *appsv1.Cluster, compSpec *appsv1.ClusterComponentSpec) {
	if cluster == nil || compSpec == nil {
		return
	}

	snapshot := map[string]int32{}
	if cluster.Annotations != nil {
		if raw, ok := cluster.Annotations[constant.SnapShotForStartAnnotationKey]; ok && raw != "" {
			if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
				compSpec.Replicas = 1
				return
			}
		}
	}

	if replicas, ok := snapshot[""]; ok && replicas > 0 {
		compSpec.Replicas = replicas
	} else {
		compSpec.Replicas = 1
	}

	for i := range compSpec.Instances {
		if replicas, ok := snapshot[compSpec.Instances[i].Name]; ok {
			compSpec.Instances[i].Replicas = &replicas
		}
	}
}
