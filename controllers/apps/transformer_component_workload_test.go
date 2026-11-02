package apps

/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

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

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/builder"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	namespace = "foo"
	name      = "bar"
)

var _ = Describe("transformer component workload", func() {

	var (
		podList                []*corev1.Pod
		pod0, pod1, pod2, pod3 *corev1.Pod
	)
	BeforeEach(func() {
		simpleNameGenerator := names.SimpleNameGenerator
		resetPods := func() {
			pod0 = builder.NewPodBuilder(name, simpleNameGenerator.GenerateName(name+"-")).
				GetObject()
			pod1 = builder.NewPodBuilder(namespace, simpleNameGenerator.GenerateName(name+"-")).
				GetObject()
			pod2 = builder.NewPodBuilder(namespace, simpleNameGenerator.GenerateName(name+"-")).
				GetObject()
			pod3 = builder.NewPodBuilder(namespace, simpleNameGenerator.GenerateName(name+"-")).
				GetObject()
		}
		resetPods()
		podList = []*corev1.Pod{pod0, pod1, pod2, pod3}
	})

	Context("Test DeletePodFromInstances", func() {
		It("Test DeletePodFromInstances", func() {
			instances := []string{pod0.Name}
			nodeAssignment := []v1alpha1.NodeAssignment{
				{
					Name: pod0.Name,
				},
				{
					Name: pod1.Name,
				},
				{
					Name: pod2.Name,
				},
				{
					Name: pod3.Name,
				},
			}
			expectedNodeAssignment := []v1alpha1.NodeAssignment{
				{
					Name: pod1.Name,
				},
				{
					Name: pod2.Name,
				},
				{
					Name: pod3.Name,
				},
			}

			newNodeAssignment, err := DeletePodFromInstances(podList, instances, 1, nodeAssignment)
			Expect(err).Should(BeNil())
			Expect(len(newNodeAssignment)).Should(Equal(3))
			for i := 0; i < len(newNodeAssignment); i++ {
				canFind := false
				for j := 0; j < len(expectedNodeAssignment); j++ {
					if newNodeAssignment[i].Name == expectedNodeAssignment[j].Name {
						canFind = true
					}
				}
				Expect(canFind).Should(Equal(true))
			}
		})
		It(
			"Test DeletePodFromInstances with no specified instances",
			func() {
				var instances []string
				nodeAssignment := []v1alpha1.NodeAssignment{
					{
						Name: pod0.Name,
					},
					{
						Name: pod1.Name,
					},
					{
						Name: pod2.Name,
					},
					{
						Name: pod3.Name,
					},
				}
				newNodeAssignment, err := DeletePodFromInstances(podList, instances, 1, nodeAssignment)
				Expect(err).Should(BeNil())
				Expect(len(newNodeAssignment)).Should(Equal(3))
			},
		)
		It("Test DeletePodFromInstances with specified one instances and delete two replicas", func() {
			instances := []string{pod0.Name}
			nodeAssignment := []v1alpha1.NodeAssignment{
				{
					Name: pod0.Name,
				},
				{
					Name: pod1.Name,
				},
				{
					Name: pod2.Name,
				},
				{
					Name: pod3.Name,
				},
			}
			newNodeAssignment, err := DeletePodFromInstances(podList, instances, 2, nodeAssignment)
			Expect(err).Should(BeNil())
			Expect(len(newNodeAssignment)).Should(Equal(2))
		})
	})

	Context("Test AllocateNodesForPod", func() {

		It("Test AllocateNodesForPod specified nodeList", func() {
			nodeList := []types.NodeName{"node1", "node2", "node3"}
			nodeAssignment := AllocateNodesForPod(nodeList, 5, "redis", "proxy")
			node1Num := 0
			node2Num := 0
			node3Num := 0
			for _, node := range nodeAssignment {
				if node.NodeSpec.NodeName == "node1" {
					node1Num++
				} else if node.NodeSpec.NodeName == "node2" {
					node2Num++
				} else if node.NodeSpec.NodeName == "node3" {
					node3Num++
				}
			}
			Expect(node1Num).Should(Equal(2))
			Expect(node2Num).Should(Equal(2))
			Expect(node3Num).Should(Equal(1))
		})
		It("Test AllocateNodesForPod no specified nodeList", func() {
			nodeAssignment := AllocateNodesForPod(nil, 5, "redis", "proxy")
			Expect(len(nodeAssignment)).Should(Equal(5))
		})
	})
})

func TestComponentWorkloadHorizontalScalePVCDeletion(t *testing.T) {
	const (
		clusterName   = "cluster"
		componentName = "component"
		volumeName    = "data"
	)
	tests := []struct {
		name            string
		currentReplicas int32
		desiredReplicas int32
		readyReplicas   int32
		wantPVCDeleted  bool
	}{
		{
			name:            "delete excess PVC when ready replicas already match desired replicas",
			currentReplicas: 2,
			desiredReplicas: 1,
			readyReplicas:   1,
			wantPVCDeleted:  true,
		},
		{
			name:            "retain PVC when scaling to zero",
			currentReplicas: 1,
			desiredReplicas: 0,
			readyReplicas:   0,
			wantPVCDeleted:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			rsmName := fmt.Sprintf("%s-%s", clusterName, componentName)
			labels := constant.GetComponentWellKnownLabels(clusterName, componentName)
			pods := []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      fmt.Sprintf("%s-0", rsmName),
						Labels:    labels,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      fmt.Sprintf("%s-1", rsmName),
						Labels:    labels,
					},
				},
			}
			pvcOrdinal := test.currentReplicas - 1
			pvc := builder.NewPVCBuilder(
				namespace,
				fmt.Sprintf("%s-%s-%d", volumeName, rsmName, pvcOrdinal),
			).GetObject()
			clientBuilder := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithObjects(&pods[0], pvc)
			if test.currentReplicas > 1 {
				clientBuilder = clientBuilder.WithObjects(&pods[1])
			}
			cli := clientBuilder.Build()
			runningRSM := &v1alpha1.ReplicatedStateMachine{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      rsmName,
				},
				Spec: v1alpha1.ReplicatedStateMachineSpec{
					Replicas:           &test.currentReplicas,
					RsmTransformPolicy: v1alpha1.ToSts,
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{ObjectMeta: metav1.ObjectMeta{Name: volumeName}},
					},
				},
				Status: v1alpha1.ReplicatedStateMachineStatus{
					StatefulSetStatus: appsv1.StatefulSetStatus{
						ReadyReplicas: test.readyReplicas,
					},
				},
			}
			cluster := &appsv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      clusterName,
				},
			}
			synthesizedComp := &component.SynthesizedComponent{
				Namespace:          namespace,
				ClusterName:        clusterName,
				Name:               componentName,
				Replicas:           test.desiredReplicas,
				RsmTransformPolicy: v1alpha1.ToSts,
			}
			dag := graph.NewDAG()
			graphCli := model.NewGraphClient(cli)
			graphCli.Root(dag, cluster.DeepCopy(), cluster, model.ActionStatusPtr())
			workloadOps := newComponentWorkloadOps(
				intctrlutil.RequestCtx{
					Ctx:      context.Background(),
					Log:      logr.Discard(),
					Recorder: record.NewFakeRecorder(1),
				},
				cli,
				cluster,
				synthesizedComp,
				runningRSM,
				nil,
				dag,
			)

			g.Expect(workloadOps.horizontalScale()).To(Succeed())
			g.Expect(graphCli.IsAction(dag, pvc, model.ActionDeletePtr())).To(Equal(test.wantPVCDeleted))
		})
	}
}

func TestMembershipManagedScaleInBlocksReverseScaleOutUntilPVCsAreClean(t *testing.T) {
	const (
		clusterName = "cluster"
		rsmName     = "cluster-dn"
		volumeName  = "data"
	)

	one, three := int32(1), int32(3)
	labels := constant.GetComponentWellKnownLabels(clusterName, "dn")
	memberStatus := []v1alpha1.MemberStatus{{
		PodName:     rsmName + "-0",
		ReplicaRole: v1alpha1.ReplicaRole{Name: "leader", IsLeader: true, CanVote: true},
	}}
	runningRSM := &v1alpha1.ReplicatedStateMachine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        rsmName,
			Generation:  4,
			Annotations: map[string]string{scaleInPVCsAnnotation: formatScaleInPVCRange(one, three)},
		},
		Spec: v1alpha1.ReplicatedStateMachineSpec{
			Replicas:             &one,
			RsmTransformPolicy:   v1alpha1.ToSts,
			Roles:                []v1alpha1.ReplicaRole{{Name: "leader", IsLeader: true, CanVote: true}, {Name: "follower", CanVote: true}},
			RoleProbe:            &v1alpha1.RoleProbe{RoleUpdateMechanism: v1alpha1.DirectAPIServerEventUpdate},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: volumeName}}},
		},
		Status: v1alpha1.ReplicatedStateMachineStatus{
			StatefulSetStatus: appsv1.StatefulSetStatus{
				ObservedGeneration: 4,
				Replicas:           1,
				ReadyReplicas:      1,
				UpdatedReplicas:    1,
			},
			InitReplicas:      1,
			ReadyInitReplicas: 1,
			CurrentGeneration: 4,
			MembersStatus:     memberStatus,
		},
	}
	protoRSM := runningRSM.DeepCopy()
	protoRSM.Spec.Replicas = &three
	protoRSM.Spec.MembershipReconfiguration = &v1alpha1.MembershipReconfiguration{
		MemberLeaveAction: &v1alpha1.Action{Command: []string{"leave"}},
	}
	component := &component.SynthesizedComponent{
		Namespace:   namespace,
		ClusterName: clusterName,
		Name:        "dn",
		Replicas:    three,
	}
	cluster := &appsv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: clusterName}}
	pvcs := []*corev1.PersistentVolumeClaim{
		builder.NewPVCBuilder(namespace, volumeName+"-"+rsmName+"-0").AddLabelsInMap(labels).GetObject(),
		builder.NewPVCBuilder(namespace, volumeName+"-"+rsmName+"-1").AddLabelsInMap(labels).GetObject(),
		builder.NewPVCBuilder(namespace, volumeName+"-"+rsmName+"-2").AddLabelsInMap(labels).GetObject(),
	}
	cli := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pvcs[0], pvcs[1], pvcs[2]).Build()
	reqCtx := intctrlutil.RequestCtx{Ctx: context.Background(), Log: logr.Discard(), Recorder: record.NewFakeRecorder(1)}
	newOps := func(dag *graph.DAG, proto *v1alpha1.ReplicatedStateMachine) *componentWorkloadOps {
		graphCli := model.NewGraphClient(cli)
		graphCli.Root(dag, cluster.DeepCopy(), cluster, model.ActionStatusPtr())
		return newComponentWorkloadOps(reqCtx, cli, cluster, component, runningRSM, proto, dag)
	}

	dag := graph.NewDAG()
	ops := newOps(dag, protoRSM)
	if err := ops.horizontalScale(); err != nil {
		t.Fatalf("horizontalScale while PVC cleanup is pending: %v", err)
	}
	graphCli := model.NewGraphClient(cli)
	if graphCli.IsAction(dag, pvcs[0], model.ActionDeletePtr()) {
		t.Fatal("scale-in cleanup must retain the PVC for ordinal 0")
	}
	for _, pvc := range pvcs[1:] {
		if !graphCli.IsAction(dag, pvc, model.ActionDeletePtr()) {
			t.Fatalf("reverse scale-out must wait for cleanup of %s", pvc.Name)
		}
	}
	if !graphCli.IsAction(dag, protoRSM, model.ActionNoopPtr()) {
		t.Fatal("RSM replica increase must be blocked while removed-member PVCs are pending cleanup")
	}

	// Simulate the API server completing the scheduled deletions. The next
	// reconcile records completion but still does not scale out in that pass.
	for _, pvc := range pvcs[1:] {
		if err := cli.Delete(context.Background(), pvc); err != nil {
			t.Fatalf("delete simulated PVC %s: %v", pvc.Name, err)
		}
	}
	protoRSM = runningRSM.DeepCopy()
	protoRSM.Spec.Replicas = &three
	protoRSM.Spec.MembershipReconfiguration = &v1alpha1.MembershipReconfiguration{
		MemberLeaveAction: &v1alpha1.Action{Command: []string{"leave"}},
	}
	dag = graph.NewDAG()
	ops = newOps(dag, protoRSM)
	if err := ops.horizontalScale(); err != nil {
		t.Fatalf("horizontalScale after PVC deletion: %v", err)
	}
	if got := protoRSM.Annotations[scaleInPVCsAnnotation]; got != scaleInPVCsComplete {
		t.Fatalf("expected cleanup completion marker, got %q", got)
	}
	if got := protoRSM.Annotations[stableMembersAnnotation]; got != "1" {
		t.Fatalf("expected stable voter count 1 before reverse scale-out, got %q", got)
	}

	// Once the cleanup marker is persisted, the next reconcile may finally
	// raise the RSM replica count and let the missing ordinal be recreated.
	runningRSM.Annotations[scaleInPVCsAnnotation] = scaleInPVCsComplete
	protoRSM = runningRSM.DeepCopy()
	protoRSM.Spec.Replicas = &three
	protoRSM.Spec.MembershipReconfiguration = &v1alpha1.MembershipReconfiguration{
		MemberLeaveAction: &v1alpha1.Action{Command: []string{"leave"}},
	}
	component.Replicas = three
	dag = graph.NewDAG()
	ops = newOps(dag, protoRSM)
	if err := ops.horizontalScale(); err != nil {
		t.Fatalf("horizontalScale after cleanup marker was persisted: %v", err)
	}
	if !graphCli.IsAction(dag, protoRSM, model.ActionUpdatePtr()) {
		t.Fatal("reverse scale-out should proceed after the old ordinal PVCs are confirmed absent")
	}
}

func TestParseScaleInPVCRangeRejectsScaleToZero(t *testing.T) {
	if _, _, _, err := parseScaleInPVCRange("0:3"); err == nil {
		t.Fatal("scale-to-zero cleanup marker must be rejected for membership-managed components")
	}
}
